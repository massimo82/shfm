/* Copyright (C) 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com>
 *
 * This file is part of shfm.
 *
 * shfm is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * shfm is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with shfm.  If not, see <https://www.gnu.org/licenses/>.
 */

/* An optional GIO module that lists the network sources shfm has open in
 * the file dialogs of GTK/GIO applications, under "Other Locations" →
 * "Networks", as gvfs does for its own mounts. There is no daemon and no
 * IPC: shfm mounts each open network source with FUSE under
 * $XDG_RUNTIME_DIR/shfm/<pid>/<name> (filesystem type fuse.rclone, see
 * networkSubtype in internal/fusemount/manager.go), and this module just
 * watches the kernel's mount table for such mounts.
 *
 * GTK files a mount under "Networks" when its default location has a URI
 * scheme other than file://, and still lets an application that only
 * accepts local files use it when the mount's root has a local path. So a
 * mount's root is its FUSE mount point, and its default location a ShfmFile
 * with an shfm:// URI that wraps the mount point. Files inside the mount
 * are ShfmFiles too — so that the dialog's path bar starts at the mount —
 * but with the file:// URI of their local path, which is what applications
 * (and the file chooser portal) get back and can open.
 *
 * The module is loaded into every GIO application, so it never touches a
 * mount itself (a hung or dead FUSE server can't block it): it only reads
 * the mount table, and hides the mounts of an shfm process that is gone
 * (after a crash; the next shfm cleans them up). With shfm not installed,
 * or no source open, it lists nothing. */

#include <errno.h>
#include <gio/gio.h>
#include <gio/gunixmounts.h>
#include <signal.h>
#include <stdlib.h>
#include <string.h>

/* GLib 2.84 renamed the GUnixMountEntry API; older GLibs (other
 * distributions) only have the old names. */
#if !GLIB_CHECK_VERSION (2, 84, 0)
#define g_unix_mount_entries_get g_unix_mounts_get
#define g_unix_mount_entry_free g_unix_mount_free
#define g_unix_mount_entry_get_mount_path g_unix_mount_get_mount_path
#define g_unix_mount_entry_get_device_path g_unix_mount_get_device_path
#define g_unix_mount_entry_get_fs_type g_unix_mount_get_fs_type
#endif
#if !GLIB_CHECK_VERSION (2, 70, 0)
#define g_spawn_check_wait_status g_spawn_check_exit_status
#endif

#define SHFM_SCHEME "shfm"
#define SHFM_FSTYPE "fuse.rclone"

typedef struct _ShfmMount ShfmMount;
typedef struct _ShfmVolumeMonitor ShfmVolumeMonitor;

/* The single monitor instance (GIO creates one per process), and the lock
 * that guards its mount list: URIs are resolved from any thread. */
static ShfmVolumeMonitor *the_monitor;
static GMutex mounts_lock;

/* ---------------------------------------------------------------------- */
/* ShfmMount                                                               */

struct _ShfmMount {
  GObject parent;
  char *path;   /* the FUSE mount point */
  char *name;   /* its base name, as shfm made it readable */
  char *source; /* the source's label, e.g. smb://nas/video */
};
typedef GObjectClass ShfmMountClass;

static void shfm_mount_iface_init (GMountIface *iface);
G_DEFINE_TYPE_WITH_CODE (ShfmMount, shfm_mount, G_TYPE_OBJECT,
                         G_IMPLEMENT_INTERFACE (G_TYPE_MOUNT, shfm_mount_iface_init))

static void
shfm_mount_finalize (GObject *object)
{
  ShfmMount *mount = (ShfmMount *) object;

  g_free (mount->path);
  g_free (mount->name);
  g_free (mount->source);
  G_OBJECT_CLASS (shfm_mount_parent_class)->finalize (object);
}

static void
shfm_mount_class_init (ShfmMountClass *klass)
{
  klass->finalize = shfm_mount_finalize;
}

static void
shfm_mount_init (ShfmMount *mount)
{
}

/* ---------------------------------------------------------------------- */
/* ShfmFile                                                                */

typedef struct {
  GObject parent;
  ShfmMount *mount; /* owned */
  GFile *local;     /* owned: the file on the FUSE mount */
} ShfmFile;
typedef GObjectClass ShfmFileClass;

static void shfm_file_iface_init (GFileIface *iface);
G_DEFINE_TYPE_WITH_CODE (ShfmFile, shfm_file, G_TYPE_OBJECT,
                         G_IMPLEMENT_INTERFACE (G_TYPE_FILE, shfm_file_iface_init))

#define SHFM_IS_FILE(o) G_TYPE_CHECK_INSTANCE_TYPE ((o), shfm_file_get_type ())

static void
shfm_file_finalize (GObject *object)
{
  ShfmFile *file = (ShfmFile *) object;

  g_clear_object (&file->mount);
  g_clear_object (&file->local);
  G_OBJECT_CLASS (shfm_file_parent_class)->finalize (object);
}

static void
shfm_file_class_init (ShfmFileClass *klass)
{
  klass->finalize = shfm_file_finalize;
}

static void
shfm_file_init (ShfmFile *file)
{
}

static GFile *
shfm_file_new (ShfmMount *mount, GFile *local)
{
  ShfmFile *file = g_object_new (shfm_file_get_type (), NULL);

  file->mount = g_object_ref (mount);
  file->local = g_object_ref (local);
  return (GFile *) file;
}

/* wrap returns local as a ShfmFile of mount if it is inside the mount (or
 * is its root), else local itself. Takes ownership of local. */
static GFile *
wrap (ShfmMount *mount, GFile *local)
{
  GFile *root, *ret;

  if (local == NULL)
    return NULL;
  root = g_file_new_for_path (mount->path);
  if (g_file_equal (local, root) || g_file_has_prefix (local, root))
    ret = shfm_file_new (mount, local);
  else
    ret = g_object_ref (local);
  g_object_unref (root);
  g_object_unref (local);
  return ret;
}

static GFile *
local_of (GFile *file)
{
  return SHFM_IS_FILE (file) ? ((ShfmFile *) file)->local : file;
}

static gboolean
is_root (GFile *file)
{
  char *path = g_file_get_path (((ShfmFile *) file)->local);
  gboolean ret = g_strcmp0 (path, ((ShfmFile *) file)->mount->path) == 0;

  g_free (path);
  return ret;
}

/* root_uri returns the shfm:// URI of a mount's root:
 * shfm://<pid>/<name>, from $XDG_RUNTIME_DIR/shfm/<pid>/<name>. */
static char *
root_uri (ShfmMount *mount)
{
  char *base = g_build_filename (g_get_user_runtime_dir (), "shfm", NULL);
  const char *rel = mount->path + strlen (base);
  char *escaped = g_uri_escape_string (rel, "/", FALSE);
  char *uri = g_strconcat (SHFM_SCHEME ":/", escaped, NULL);

  g_free (base);
  g_free (escaped);
  return uri;
}

static GFile *
f_dup (GFile *file)
{
  return shfm_file_new (((ShfmFile *) file)->mount, ((ShfmFile *) file)->local);
}

static guint
f_hash (GFile *file)
{
  return g_file_hash (local_of (file));
}

static gboolean
f_equal (GFile *a, GFile *b)
{
  return SHFM_IS_FILE (b) && g_file_equal (local_of (a), local_of (b));
}

static gboolean
f_is_native (GFile *file)
{
  return !is_root (file);
}

static char *
f_get_uri_scheme (GFile *file)
{
  return g_strdup (is_root (file) ? SHFM_SCHEME : "file");
}

static gboolean
f_has_uri_scheme (GFile *file, const char *scheme)
{
  char *own = f_get_uri_scheme (file);
  gboolean ret = g_ascii_strcasecmp (own, scheme) == 0;

  g_free (own);
  return ret;
}

static char *
f_get_basename (GFile *file)
{
  return g_file_get_basename (local_of (file));
}

static char *
f_get_path (GFile *file)
{
  return g_file_get_path (local_of (file));
}

static char *
f_get_uri (GFile *file)
{
  if (is_root (file))
    return root_uri (((ShfmFile *) file)->mount);
  return g_file_get_uri (local_of (file));
}

static char *
f_get_parse_name (GFile *file)
{
  if (is_root (file))
    return root_uri (((ShfmFile *) file)->mount);
  return g_file_get_parse_name (local_of (file));
}

static GFile *
f_get_parent (GFile *file)
{
  if (is_root (file))
    return NULL;
  return wrap (((ShfmFile *) file)->mount, g_file_get_parent (local_of (file)));
}

static gboolean
f_prefix_matches (GFile *prefix, GFile *file)
{
  return g_file_has_prefix (local_of (file), local_of (prefix));
}

static char *
f_get_relative_path (GFile *parent, GFile *descendant)
{
  return g_file_get_relative_path (local_of (parent), local_of (descendant));
}

static GFile *
f_resolve_relative_path (GFile *file, const char *relative_path)
{
  return wrap (((ShfmFile *) file)->mount,
               g_file_resolve_relative_path (local_of (file), relative_path));
}

static GFile *
f_get_child_for_display_name (GFile *file, const char *display_name, GError **error)
{
  return wrap (((ShfmFile *) file)->mount,
               g_file_get_child_for_display_name (local_of (file), display_name, error));
}

static GFileEnumerator *
f_enumerate_children (GFile *file, const char *attributes, GFileQueryInfoFlags flags,
                      GCancellable *cancellable, GError **error)
{
  return g_file_enumerate_children (local_of (file), attributes, flags, cancellable, error);
}

static GFileInfo *
f_query_info (GFile *file, const char *attributes, GFileQueryInfoFlags flags,
              GCancellable *cancellable, GError **error)
{
  GFileInfo *info = g_file_query_info (local_of (file), attributes, flags, cancellable, error);

  if (info != NULL && is_root (file))
    g_file_info_set_display_name (info, ((ShfmFile *) file)->mount->name);
  return info;
}

static GFileInfo *
f_query_filesystem_info (GFile *file, const char *attributes, GCancellable *cancellable,
                         GError **error)
{
  GFileInfo *info = g_file_query_filesystem_info (local_of (file), attributes, cancellable, error);

  if (info != NULL)
    g_file_info_set_attribute_boolean (info, G_FILE_ATTRIBUTE_FILESYSTEM_REMOTE, TRUE);
  return info;
}

static GMount *
f_find_enclosing_mount (GFile *file, GCancellable *cancellable, GError **error)
{
  return g_object_ref (G_MOUNT (((ShfmFile *) file)->mount));
}

static GFile *
f_set_display_name (GFile *file, const char *display_name, GCancellable *cancellable,
                    GError **error)
{
  return wrap (((ShfmFile *) file)->mount,
               g_file_set_display_name (local_of (file), display_name, cancellable, error));
}

static GFileAttributeInfoList *
f_query_settable_attributes (GFile *file, GCancellable *cancellable, GError **error)
{
  return g_file_query_settable_attributes (local_of (file), cancellable, error);
}

static GFileAttributeInfoList *
f_query_writable_namespaces (GFile *file, GCancellable *cancellable, GError **error)
{
  return g_file_query_writable_namespaces (local_of (file), cancellable, error);
}

static gboolean
f_set_attribute (GFile *file, const char *attribute, GFileAttributeType type,
                 gpointer value_p, GFileQueryInfoFlags flags, GCancellable *cancellable,
                 GError **error)
{
  return g_file_set_attribute (local_of (file), attribute, type, value_p, flags, cancellable, error);
}

static gboolean
f_set_attributes_from_info (GFile *file, GFileInfo *info, GFileQueryInfoFlags flags,
                            GCancellable *cancellable, GError **error)
{
  return g_file_set_attributes_from_info (local_of (file), info, flags, cancellable, error);
}

static GFileInputStream *
f_read (GFile *file, GCancellable *cancellable, GError **error)
{
  return g_file_read (local_of (file), cancellable, error);
}

static GFileOutputStream *
f_append_to (GFile *file, GFileCreateFlags flags, GCancellable *cancellable, GError **error)
{
  return g_file_append_to (local_of (file), flags, cancellable, error);
}

static GFileOutputStream *
f_create (GFile *file, GFileCreateFlags flags, GCancellable *cancellable, GError **error)
{
  return g_file_create (local_of (file), flags, cancellable, error);
}

static GFileOutputStream *
f_replace (GFile *file, const char *etag, gboolean make_backup, GFileCreateFlags flags,
           GCancellable *cancellable, GError **error)
{
  return g_file_replace (local_of (file), etag, make_backup, flags, cancellable, error);
}

static GFileIOStream *
f_open_readwrite (GFile *file, GCancellable *cancellable, GError **error)
{
  return g_file_open_readwrite (local_of (file), cancellable, error);
}

static GFileIOStream *
f_create_readwrite (GFile *file, GFileCreateFlags flags, GCancellable *cancellable,
                    GError **error)
{
  return g_file_create_readwrite (local_of (file), flags, cancellable, error);
}

static GFileIOStream *
f_replace_readwrite (GFile *file, const char *etag, gboolean make_backup,
                     GFileCreateFlags flags, GCancellable *cancellable, GError **error)
{
  return g_file_replace_readwrite (local_of (file), etag, make_backup, flags, cancellable, error);
}

static gboolean
f_delete_file (GFile *file, GCancellable *cancellable, GError **error)
{
  return g_file_delete (local_of (file), cancellable, error);
}

static gboolean
f_trash (GFile *file, GCancellable *cancellable, GError **error)
{
  return g_file_trash (local_of (file), cancellable, error);
}

static gboolean
f_make_directory (GFile *file, GCancellable *cancellable, GError **error)
{
  return g_file_make_directory (local_of (file), cancellable, error);
}

static GFileMonitor *
f_monitor_dir (GFile *file, GFileMonitorFlags flags, GCancellable *cancellable, GError **error)
{
  return g_file_monitor_directory (local_of (file), flags, cancellable, error);
}

static GFileMonitor *
f_monitor_file (GFile *file, GFileMonitorFlags flags, GCancellable *cancellable, GError **error)
{
  return g_file_monitor_file (local_of (file), flags, cancellable, error);
}

static void
shfm_file_iface_init (GFileIface *iface)
{
  iface->dup = f_dup;
  iface->hash = f_hash;
  iface->equal = f_equal;
  iface->is_native = f_is_native;
  iface->has_uri_scheme = f_has_uri_scheme;
  iface->get_uri_scheme = f_get_uri_scheme;
  iface->get_basename = f_get_basename;
  iface->get_path = f_get_path;
  iface->get_uri = f_get_uri;
  iface->get_parse_name = f_get_parse_name;
  iface->get_parent = f_get_parent;
  iface->prefix_matches = f_prefix_matches;
  iface->get_relative_path = f_get_relative_path;
  iface->resolve_relative_path = f_resolve_relative_path;
  iface->get_child_for_display_name = f_get_child_for_display_name;
  iface->enumerate_children = f_enumerate_children;
  iface->query_info = f_query_info;
  iface->query_filesystem_info = f_query_filesystem_info;
  iface->find_enclosing_mount = f_find_enclosing_mount;
  iface->set_display_name = f_set_display_name;
  iface->query_settable_attributes = f_query_settable_attributes;
  iface->query_writable_namespaces = f_query_writable_namespaces;
  iface->set_attribute = f_set_attribute;
  iface->set_attributes_from_info = f_set_attributes_from_info;
  iface->read_fn = f_read;
  iface->append_to = f_append_to;
  iface->create = f_create;
  iface->replace = f_replace;
  iface->open_readwrite = f_open_readwrite;
  iface->create_readwrite = f_create_readwrite;
  iface->replace_readwrite = f_replace_readwrite;
  iface->delete_file = f_delete_file;
  iface->trash = f_trash;
  iface->make_directory = f_make_directory;
  iface->monitor_dir = f_monitor_dir;
  iface->monitor_file = f_monitor_file;
}

/* ---------------------------------------------------------------------- */
/* ShfmMount's GMount interface                                            */

static GFile *
m_get_root (GMount *mount)
{
  return g_file_new_for_path (((ShfmMount *) mount)->path);
}

static GFile *
m_get_default_location (GMount *mount)
{
  GFile *local = g_file_new_for_path (((ShfmMount *) mount)->path);
  GFile *file = shfm_file_new ((ShfmMount *) mount, local);

  g_object_unref (local);
  return file;
}

static char *
m_get_name (GMount *mount)
{
  return g_strdup (((ShfmMount *) mount)->name);
}

static GIcon *
m_get_icon (GMount *mount)
{
  return g_themed_icon_new_with_default_fallbacks ("folder-remote");
}

static GIcon *
m_get_symbolic_icon (GMount *mount)
{
  return g_themed_icon_new_with_default_fallbacks ("folder-remote-symbolic");
}

static char *
m_get_uuid (GMount *mount)
{
  return NULL;
}

static GVolume *
m_get_volume (GMount *mount)
{
  return NULL;
}

static GDrive *
m_get_drive (GMount *mount)
{
  return NULL;
}

static gboolean
m_can_unmount (GMount *mount)
{
  return TRUE;
}

static gboolean
m_can_eject (GMount *mount)
{
  return FALSE;
}

static void
unmount_thread (GTask *task, gpointer source, gpointer data, GCancellable *cancellable)
{
  const char *argv[] = { "fusermount3", "-u", ((ShfmMount *) source)->path, NULL };
  GError *error = NULL;
  char *err_out = NULL;
  int status;

  if (!g_spawn_sync (NULL, (char **) argv, NULL, G_SPAWN_SEARCH_PATH | G_SPAWN_STDOUT_TO_DEV_NULL,
                     NULL, NULL, NULL, &err_out, &status, &error))
    {
      g_task_return_error (task, error);
    }
  else if (!g_spawn_check_wait_status (status, NULL))
    {
      g_strstrip (err_out);
      /* fusermount3's own message says why, e.g. "Device or resource busy". */
      g_task_return_new_error (task, G_IO_ERROR, G_IO_ERROR_BUSY, "%s",
                               *err_out ? err_out : "Could not unmount");
    }
  else
    {
      g_task_return_boolean (task, TRUE);
    }
  g_free (err_out);
}

/* Unmounting is only fusermount3 -u: shfm notices its FUSE server stopped,
 * and closes the mount's connection. */
static void
m_unmount_with_operation (GMount *mount, GMountUnmountFlags flags,
                          GMountOperation *mount_operation, GCancellable *cancellable,
                          GAsyncReadyCallback callback, gpointer user_data)
{
  GTask *task = g_task_new (mount, cancellable, callback, user_data);

  g_task_run_in_thread (task, unmount_thread);
  g_object_unref (task);
}

static gboolean
m_unmount_with_operation_finish (GMount *mount, GAsyncResult *result, GError **error)
{
  return g_task_propagate_boolean (G_TASK (result), error);
}

static void
m_unmount (GMount *mount, GMountUnmountFlags flags, GCancellable *cancellable,
           GAsyncReadyCallback callback, gpointer user_data)
{
  m_unmount_with_operation (mount, flags, NULL, cancellable, callback, user_data);
}

static gboolean
m_unmount_finish (GMount *mount, GAsyncResult *result, GError **error)
{
  return g_task_propagate_boolean (G_TASK (result), error);
}

static void
shfm_mount_iface_init (GMountIface *iface)
{
  iface->get_root = m_get_root;
  iface->get_default_location = m_get_default_location;
  iface->get_name = m_get_name;
  iface->get_icon = m_get_icon;
  iface->get_symbolic_icon = m_get_symbolic_icon;
  iface->get_uuid = m_get_uuid;
  iface->get_volume = m_get_volume;
  iface->get_drive = m_get_drive;
  iface->can_unmount = m_can_unmount;
  iface->can_eject = m_can_eject;
  iface->unmount = m_unmount;
  iface->unmount_finish = m_unmount_finish;
  iface->unmount_with_operation = m_unmount_with_operation;
  iface->unmount_with_operation_finish = m_unmount_with_operation_finish;
}

/* ---------------------------------------------------------------------- */
/* ShfmVolumeMonitor                                                       */

struct _ShfmVolumeMonitor {
  GVolumeMonitor parent;
  GUnixMountMonitor *unix_monitor;
  GList *mounts; /* ShfmMount*, guarded by mounts_lock */
};
typedef GVolumeMonitorClass ShfmVolumeMonitorClass;

G_DEFINE_TYPE (ShfmVolumeMonitor, shfm_volume_monitor, G_TYPE_VOLUME_MONITOR)

static ShfmMount *
find_mount (GList *mounts, const char *path, const char *source)
{
  for (GList *l = mounts; l != NULL; l = l->next)
    {
      ShfmMount *mount = l->data;
      if ((path != NULL && strcmp (mount->path, path) == 0) ||
          (source != NULL && g_strcmp0 (mount->source, source) == 0))
        return mount;
    }
  return NULL;
}

/* is_shfm_mount_path reports whether path is <base>/<pid>/<name>. */
static gboolean
is_shfm_mount_path (const char *path, const char *base)
{
  size_t len = strlen (base);
  const char *rest, *slash;

  if (path == NULL || strncmp (path, base, len) != 0 || path[len] != '/')
    return FALSE;
  rest = path + len + 1;
  slash = strchr (rest, '/');
  if (slash == NULL || slash == rest || strchr (slash + 1, '/') != NULL || slash[1] == '\0')
    return FALSE;
  for (const char *p = rest; p < slash; p++)
    if (!g_ascii_isdigit (*p))
      return FALSE;
  return TRUE;
}

/* owner_alive reports whether the shfm process that made the mount at
 * <base>/<pid>/<name> still runs: a mount whose FUSE server is gone only
 * fails with "Transport endpoint is not connected". */
static gboolean
owner_alive (const char *path, const char *base)
{
  long pid = strtol (path + strlen (base) + 1, NULL, 10);

  return pid > 0 && (kill ((pid_t) pid, 0) == 0 || errno == EPERM);
}

/* refresh rebuilds the mount list from the kernel's mount table, emitting
 * mount-added/mount-removed for what changed (unless !emit). */
static void
refresh (ShfmVolumeMonitor *monitor, gboolean emit)
{
  char *base = g_build_filename (g_get_user_runtime_dir (), "shfm", NULL);
  GList *entries = g_unix_mount_entries_get (NULL);
  GList *old, *fresh = NULL, *added = NULL;

  g_mutex_lock (&mounts_lock);
  old = monitor->mounts;
  for (GList *l = entries; l != NULL; l = l->next)
    {
      GUnixMountEntry *entry = l->data;
      const char *path = g_unix_mount_entry_get_mount_path (entry);
      const char *source = g_unix_mount_entry_get_device_path (entry);
      ShfmMount *mount;

      if (g_strcmp0 (g_unix_mount_entry_get_fs_type (entry), SHFM_FSTYPE) != 0 ||
          !is_shfm_mount_path (path, base) || !owner_alive (path, base))
        continue;
      /* Two shfm processes with the same source open: list it once. */
      if (find_mount (fresh, NULL, source) != NULL)
        continue;
      mount = find_mount (old, path, NULL);
      if (mount != NULL)
        {
          old = g_list_remove (old, mount);
        }
      else
        {
          mount = g_object_new (shfm_mount_get_type (), NULL);
          mount->path = g_strdup (path);
          mount->name = g_path_get_basename (path);
          mount->source = g_strdup (source);
          added = g_list_prepend (added, g_object_ref (mount));
        }
      fresh = g_list_prepend (fresh, mount);
    }
  monitor->mounts = g_list_reverse (fresh);
  g_mutex_unlock (&mounts_lock);

  /* Signals are emitted without the lock: handlers may call back in. */
  for (GList *l = old; l != NULL; l = l->next)
    {
      if (emit)
        {
          g_signal_emit_by_name (l->data, "unmounted");
          g_signal_emit_by_name (monitor, "mount-removed", l->data);
        }
    }
  for (GList *l = added; l != NULL; l = l->next)
    if (emit)
      g_signal_emit_by_name (monitor, "mount-added", l->data);

  g_list_free_full (old, g_object_unref);
  g_list_free_full (added, g_object_unref);
  g_list_free_full (entries, (GDestroyNotify) g_unix_mount_entry_free);
  g_free (base);
}

static void
on_mounts_changed (GUnixMountMonitor *unix_monitor, gpointer monitor)
{
  refresh (monitor, TRUE);
}

/* lookup_uri resolves an shfm:// URI (or parse name) of a mount's root:
 * shfm://<pid>/<name> -> $XDG_RUNTIME_DIR/shfm/<pid>/<name>. Anything that
 * isn't one of the mounts listed (any more) resolves to a path in shfm's
 * folder that doesn't exist, so that opening it fails normally. */
static GFile *
lookup_uri (GVfs *vfs, const char *uri, gpointer data)
{
  const char *prefix = SHFM_SCHEME "://";
  char *base = g_build_filename (g_get_user_runtime_dir (), "shfm", NULL);
  /* An escaped "/" makes the unescaping fail: a name has none. */
  char *rel = g_ascii_strncasecmp (uri, prefix, strlen (prefix)) == 0
                ? g_uri_unescape_string (uri + strlen (prefix), "/")
                : NULL;
  char *path = rel != NULL ? g_canonicalize_filename (rel, base) : NULL;
  GFile *ret = NULL;
  ShfmMount *mount;

  g_mutex_lock (&mounts_lock);
  mount = the_monitor != NULL && path != NULL ? find_mount (the_monitor->mounts, path, NULL) : NULL;
  if (mount != NULL)
    {
      GFile *local = g_file_new_for_path (path);
      ret = shfm_file_new (mount, local);
      g_object_unref (local);
    }
  g_mutex_unlock (&mounts_lock);

  if (ret == NULL)
    {
      char *missing = g_build_filename (base, ".not-mounted", NULL);
      ret = g_file_new_for_path (missing);
      g_free (missing);
    }
  g_free (path);
  g_free (rel);
  g_free (base);
  return ret;
}

static GList *
vm_get_mounts (GVolumeMonitor *volume_monitor)
{
  char *base = g_build_filename (g_get_user_runtime_dir (), "shfm", NULL);
  GList *ret = NULL;

  /* Checked again here: a crashed shfm leaves its mounts in the table,
   * with no event to refresh on. */
  g_mutex_lock (&mounts_lock);
  for (GList *l = ((ShfmVolumeMonitor *) volume_monitor)->mounts; l != NULL; l = l->next)
    if (owner_alive (((ShfmMount *) l->data)->path, base))
      ret = g_list_prepend (ret, g_object_ref (l->data));
  g_mutex_unlock (&mounts_lock);
  g_free (base);
  return g_list_reverse (ret);
}

static GList *
vm_get_nothing (GVolumeMonitor *volume_monitor)
{
  return NULL;
}

static GVolume *
vm_get_volume_for_uuid (GVolumeMonitor *volume_monitor, const char *uuid)
{
  return NULL;
}

static GMount *
vm_get_mount_for_uuid (GVolumeMonitor *volume_monitor, const char *uuid)
{
  return NULL;
}

static gboolean
vm_is_supported (void)
{
  return TRUE;
}

static void
shfm_volume_monitor_dispose (GObject *object)
{
  ShfmVolumeMonitor *monitor = (ShfmVolumeMonitor *) object;

  if (monitor->unix_monitor != NULL)
    g_signal_handlers_disconnect_by_func (monitor->unix_monitor, on_mounts_changed, monitor);
  g_clear_object (&monitor->unix_monitor);
  g_mutex_lock (&mounts_lock);
  if (the_monitor == monitor)
    the_monitor = NULL;
  g_list_free_full (g_steal_pointer (&monitor->mounts), g_object_unref);
  g_mutex_unlock (&mounts_lock);
  G_OBJECT_CLASS (shfm_volume_monitor_parent_class)->dispose (object);
}

static void
shfm_volume_monitor_class_init (ShfmVolumeMonitorClass *klass)
{
  G_OBJECT_CLASS (klass)->dispose = shfm_volume_monitor_dispose;
  klass->is_supported = vm_is_supported;
  klass->get_mounts = vm_get_mounts;
  klass->get_connected_drives = vm_get_nothing;
  klass->get_volumes = vm_get_nothing;
  klass->get_volume_for_uuid = vm_get_volume_for_uuid;
  klass->get_mount_for_uuid = vm_get_mount_for_uuid;
}

static void
shfm_volume_monitor_init (ShfmVolumeMonitor *monitor)
{
  static gsize scheme_registered = 0;

  monitor->unix_monitor = g_unix_mount_monitor_get ();
  g_signal_connect (monitor->unix_monitor, "mounts-changed", G_CALLBACK (on_mounts_changed), monitor);
  refresh (monitor, FALSE);

  g_mutex_lock (&mounts_lock);
  the_monitor = monitor;
  g_mutex_unlock (&mounts_lock);

  /* Not in g_io_module_load: getting the default GVfs there could load
   * the GIO modules again. */
  if (g_once_init_enter (&scheme_registered))
    {
      g_vfs_register_uri_scheme (g_vfs_get_default (), SHFM_SCHEME,
                                 lookup_uri, NULL, NULL, lookup_uri, NULL, NULL);
      g_once_init_leave (&scheme_registered, 1);
    }
}

/* ---------------------------------------------------------------------- */
/* Module entry points                                                     */

G_MODULE_EXPORT void
g_io_module_load (GIOModule *module)
{
  /* The types are registered statically: the module must stay loaded. */
  g_type_module_use (G_TYPE_MODULE (module));
  g_io_extension_point_implement (G_VOLUME_MONITOR_EXTENSION_POINT_NAME,
                                  shfm_volume_monitor_get_type (), "shfm", 0);
}

G_MODULE_EXPORT void
g_io_module_unload (GIOModule *module)
{
}

G_MODULE_EXPORT char **
g_io_module_query (void)
{
  char *extension_points[] = { G_VOLUME_MONITOR_EXTENSION_POINT_NAME, NULL };

  return g_strdupv (extension_points);
}
