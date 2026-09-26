// Copyright (C) 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com>
//
// This file is part of shfm.
//
// shfm is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// shfm is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with shfm.  If not, see <https://www.gnu.org/licenses/>.

//go:build linux

package polkitagent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	authorityName  = "org.freedesktop.PolicyKit1"
	authorityPath  = "/org/freedesktop/PolicyKit1/Authority"
	authorityIface = "org.freedesktop.PolicyKit1.Authority"
	agentIface     = "org.freedesktop.PolicyKit1.AuthenticationAgent"
	agentPath      = "/org/shfm/PolicyKit1/AuthenticationAgent"
	errCancelled   = "org.freedesktop.PolicyKit1.Error.Cancelled"

	// maxAttempts is how many passwords are tried before giving up, as
	// polkit's own agents do.
	maxAttempts = 3
)

// The helper that checks the password with PAM and reports the outcome to
// polkitd: reached through its socket-activated service (polkit ≥ 126),
// or run directly (setuid root) on older setups. Variables so tests can
// point them at fakes.
var (
	helperSocket = "/run/polkit/agent-helper.socket"
	helperPath   = "/usr/lib/polkit-1/polkit-agent-helper-1"
)

// subject is polkit's (sa{sv}) Subject, and identity its Identity.
type subject struct {
	Kind    string
	Details map[string]dbus.Variant
}

type identity struct {
	Kind    string
	Details map[string]dbus.Variant
}

// Agent is a registered authentication agent; Close unregisters it.
type Agent struct {
	conn   *dbus.Conn
	self   subject
	prompt Prompter

	auth sync.Mutex // one authentication (so one dialog) at a time

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // by cookie
}

// Start registers an agent for shfm's own process: pkexec, run by shfm,
// asks it for authentication, while every other program keeps using the
// session's agent, if any.
func Start(prompt Prompter) (*Agent, error) {
	self, err := selfSubject()
	if err != nil {
		return nil, err
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	a := &Agent{conn: conn, self: self, prompt: prompt, cancels: map[string]context.CancelFunc{}}
	if err := conn.ExportMethodTable(map[string]any{
		"BeginAuthentication":  a.begin,
		"CancelAuthentication": a.cancel,
	}, agentPath, agentIface); err != nil {
		conn.Close()
		return nil, err
	}
	call := conn.Object(authorityName, authorityPath).Call(authorityIface+".RegisterAuthenticationAgent", 0,
		self, locale(), agentPath)
	if call.Err != nil {
		conn.Close()
		return nil, fmt.Errorf("registering the polkit agent: %w", call.Err)
	}
	return a, nil
}

// Close unregisters the agent (polkitd would also notice the connection
// going away) and closes its bus connection.
func (a *Agent) Close() {
	a.conn.Object(authorityName, authorityPath).Call(authorityIface+".UnregisterAuthenticationAgent", 0,
		a.self, agentPath)
	a.conn.Close()
}

// begin implements BeginAuthentication. polkitd waits for it to return:
// nil once the helper has reported the outcome (success or not — polkitd
// learns which from the helper itself), Cancelled when the user dismissed
// the prompt or polkitd withdrew the request.
func (a *Agent) begin(actionID, message, iconName string, details map[string]string, cookie string, identities []identity) *dbus.Error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.mu.Lock()
	a.cancels[cookie] = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.cancels, cookie)
		a.mu.Unlock()
	}()

	a.auth.Lock()
	defer a.auth.Unlock()
	if ctx.Err() != nil {
		return dbus.NewError(errCancelled, []any{"cancelled"})
	}

	name, err := pickUser(identities)
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	var notice string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ok, err := a.session(ctx, message, name, cookie, &notice)
		switch {
		case ok:
			return nil
		case errors.Is(err, errDismissed) || ctx.Err() != nil:
			return dbus.NewError(errCancelled, []any{"dismissed"})
		case err != nil:
			return dbus.MakeFailedError(err)
		}
		notice = strings.TrimSpace(notice + "\nAuthentication failed, try again.")
	}
	return nil
}

// cancel implements CancelAuthentication.
func (a *Agent) cancel(cookie string) *dbus.Error {
	a.mu.Lock()
	if c, ok := a.cancels[cookie]; ok {
		c()
	}
	a.mu.Unlock()
	return nil
}

var errDismissed = errors.New("dismissed")

// session runs one attempt: the helper asks PAM's questions (usually just
// the password), relayed to the user, and ends with SUCCESS or FAILURE.
// notice carries PAM's informational/error messages to the next prompt.
func (a *Agent) session(ctx context.Context, message, name, cookie string, notice *string) (bool, error) {
	rw, err := openHelper(name, cookie)
	if err != nil {
		return false, err
	}
	defer rw.Close()
	stop := context.AfterFunc(ctx, func() { rw.Close() })
	defer stop()

	r := bufio.NewReader(rw)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, fmt.Errorf("polkit helper: %w", err)
		}
		line = unescape(strings.TrimSuffix(line, "\n"))
		kind, text, _ := strings.Cut(line, " ")
		switch kind {
		case "PAM_PROMPT_ECHO_OFF", "PAM_PROMPT_ECHO_ON":
			resp, ok := a.prompt(ctx, Prompt{
				Message: message, User: name, Text: text,
				Echo: kind == "PAM_PROMPT_ECHO_ON", Notice: *notice,
			})
			*notice = ""
			if !ok {
				return false, errDismissed
			}
			if _, err := io.WriteString(rw, resp+"\n"); err != nil {
				return false, fmt.Errorf("polkit helper: %w", err)
			}
		case "PAM_ERROR_MSG", "PAM_TEXT_INFO":
			*notice = strings.TrimSpace(*notice + "\n" + text)
		case "SUCCESS":
			return true, nil
		case "FAILURE":
			return false, nil
		default:
			return false, fmt.Errorf("polkit helper: unexpected %q", line)
		}
	}
}

// openHelper starts a helper session for user and cookie: through the
// socket, the helper reads "<user>\n<cookie>\n"; run directly, it takes
// the user as its argument and the cookie on stdin (never on the command
// line, where other processes could read it).
func openHelper(name, cookie string) (io.ReadWriteCloser, error) {
	if c, err := net.Dial("unix", helperSocket); err == nil {
		if _, err := io.WriteString(c, name+"\n"+cookie+"\n"); err != nil {
			c.Close()
			return nil, err
		}
		return c, nil
	}
	cmd := exec.Command(helperPath, name)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("polkit helper: %w", err)
	}
	h := &spawnedHelper{cmd: cmd, stdin: stdin, stdout: stdout}
	if _, err := io.WriteString(stdin, cookie+"\n"); err != nil {
		h.Close()
		return nil, err
	}
	return h, nil
}

type spawnedHelper struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	once   sync.Once
}

func (h *spawnedHelper) Read(p []byte) (int, error)  { return h.stdout.Read(p) }
func (h *spawnedHelper) Write(p []byte) (int, error) { return h.stdin.Write(p) }

func (h *spawnedHelper) Close() error {
	h.once.Do(func() {
		h.stdin.Close()
		h.cmd.Process.Kill()
		h.cmd.Wait()
	})
	return nil
}

// pickUser chooses whose password to ask for among the identities polkit
// accepts: the current user when allowed (the usual case, an admin),
// otherwise the first one listed (e.g. root).
func pickUser(identities []identity) (string, error) {
	var uids []uint32
	for _, id := range identities {
		if id.Kind != "unix-user" {
			continue
		}
		if uid, ok := id.Details["uid"].Value().(uint32); ok {
			uids = append(uids, uid)
		}
	}
	if len(uids) == 0 {
		return "", errors.New("no user can authenticate for this action")
	}
	uid := uids[0]
	me := uint32(os.Getuid())
	for _, u := range uids {
		if u == me {
			uid = u
		}
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// selfSubject is shfm's process as a polkit subject: pid plus start time
// (so a recycled pid can't be mistaken for it), from /proc/self/stat.
func selfSubject() (subject, error) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return subject{}, err
	}
	start, err := parseStartTime(string(data))
	if err != nil {
		return subject{}, err
	}
	return subject{Kind: "unix-process", Details: map[string]dbus.Variant{
		"pid":        dbus.MakeVariant(uint32(os.Getpid())),
		"start-time": dbus.MakeVariant(start),
	}}, nil
}

// parseStartTime extracts field 22 (starttime) of a /proc/<pid>/stat line.
// The command name (field 2) is in parentheses and may contain spaces, so
// fields are counted from the last ')'.
func parseStartTime(stat string) (uint64, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("malformed /proc/self/stat")
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < 20 {
		return 0, errors.New("malformed /proc/self/stat")
	}
	return strconv.ParseUint(f[19], 10, 64)
}

func locale() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return "C"
}

// unescape reverses the g_strescape the helper applies to its lines
// (GLib's g_strcompress): \b \f \n \r \t \v \\ \" and octal \NNN, which is
// also how non-ASCII bytes arrive.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch c = s[i]; c {
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v := 0
			j := i
			for ; j < len(s) && j < i+3 && s[j] >= '0' && s[j] <= '7'; j++ {
				v = v*8 + int(s[j]-'0')
			}
			b.WriteByte(byte(v))
			i = j - 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
