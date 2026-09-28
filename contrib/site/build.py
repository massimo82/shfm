#!/usr/bin/env python3
"""Turns README.md into the MkDocs project of shfm's website.

The README stays the only source: each "## " section becomes a page (what
comes before the first one, and "Features", the home page; what's between
<!-- site:skip --> and <!-- /site:skip --> is left out), links between
sections are pointed at the page holding their target, and links to files
of the repository at GitHub. Writes OUT/mkdocs.yml, from contrib/site/
mkdocs.yml plus the pages' navigation, and OUT/docs/; then
"mkdocs build -f OUT/mkdocs.yml" makes the site.

    contrib/site/build.py OUT
"""

import re
import shutil
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
REPO = "https://github.com/massimo82/shfm"
# Sections that stay on the home page rather than getting their own
HOME_SECTIONS = {"Features"}

FENCE = re.compile(r"^\s*(```|~~~)")
HEADING = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
LINK = re.compile(r"(!?\[(?:[^\]\[]|\[[^\]]*\])*\])\(([^)\s]+)\)")


def slug(title):
    """The anchor GitHub gives a heading, which the site's slugify
    (pymdownx.slugs, case lower) reproduces."""
    title = re.sub(r"`([^`]*)`", r"\1", title)
    title = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", title)
    title = title.strip().lower()
    title = re.sub(r"[^\w\- ]", "", title)
    return title.replace(" ", "-")


def page_name(title):
    return slug(title) + ".md"


def split(readme):
    """Returns the title, and the pages as [title, file, lines], the home
    page first with title None. Headings inside code blocks don't count."""
    pages = [[None, "index.md", []]]
    title = None
    fenced = False
    for line in readme.splitlines():
        if FENCE.match(line):
            fenced = not fenced
        m = None if fenced else HEADING.match(line)
        if m and len(m.group(1)) == 1 and title is None:
            title = m.group(2)
            continue
        if m and len(m.group(1)) == 2 and m.group(2) not in HOME_SECTIONS:
            pages.append([m.group(2), page_name(m.group(2)), []])
        pages[-1][2].append(line)
    return title, pages


def anchors(pages):
    """Maps every heading's anchor to the page holding it."""
    where = {}
    for _, name, lines in pages:
        fenced = False
        for line in lines:
            if FENCE.match(line):
                fenced = not fenced
            m = None if fenced else HEADING.match(line)
            if m:
                where.setdefault(slug(m.group(2)), name)
    return where


def relink(lines, name, where):
    """Points links to sections at their page, and links to the repository's
    files at GitHub; the screenshot and other docs/ assets are copied."""
    def fix(m):
        text, target = m.group(1), m.group(2)
        if re.match(r"^[a-z]+:", target):
            return m.group(0)
        if target.startswith("#"):
            page = where.get(target[1:])
            if page is None:
                print(f"warning: no heading for link {target}", file=sys.stderr)
                return m.group(0)
            if page == name:
                return f"{text}({target})"
            return f"{text}({page}{target})"
        if target.startswith("docs/"):
            return f"{text}({target[len('docs/'):]})"
        kind = "tree" if (ROOT / target).is_dir() else "blob"
        return f"{text}({REPO}/{kind}/main/{target})"

    out, fenced = [], False
    for line in lines:
        if FENCE.match(line):
            fenced = not fenced
        out.append(line if fenced else LINK.sub(fix, line))
    return out


def promote(lines):
    """Raises a section page's headings one level: its "## " heading
    becomes the page's title."""
    out, fenced = [], False
    for line in lines:
        if FENCE.match(line):
            fenced = not fenced
        if not fenced and HEADING.match(line) and line.startswith("##"):
            line = line[1:]
        out.append(line)
    return out


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    out = Path(sys.argv[1])
    docs = out / "docs"
    if docs.exists():
        shutil.rmtree(docs)
    docs.mkdir(parents=True)

    readme = (ROOT / "README.md").read_text()
    # What's only for readers of the README itself (the link to this site)
    readme = re.sub(r"<!-- site:skip -->.*?<!-- /site:skip -->\n*", "", readme, flags=re.S)
    title, pages = split(readme)
    where = anchors(pages)
    version = re.search(r"v(\d+\.\d+\.\d+)", title or "")

    for page_title, name, lines in pages:
        body = relink(lines, name, where)
        if page_title is None:
            body = home_intro(version.group(1) if version else None) + body
        else:
            body = promote(body)
        (docs / name).write_text("\n".join(body).strip() + "\n")

    for asset in (ROOT / "docs").iterdir():
        if asset.is_file():
            shutil.copy(asset, docs / asset.name)
    shutil.copy(ROOT / "contrib/site/extra.css", docs / "extra.css")

    nav = ["nav:", "  - Home: index.md"]
    nav += [f"  - {t!r}: {n}" for t, n, _ in pages[1:]]
    config = (ROOT / "contrib/site/mkdocs.yml").read_text()
    (out / "mkdocs.yml").write_text(config.rstrip() + "\n\n" + "\n".join(nav) + "\n")


def home_intro(version):
    release = f"Download {version}" if version else "Download"
    return [
        "# shfm — Shell File Manager",
        "",
        f"[{release}]({REPO}/releases/latest){{ .md-button .md-button--primary }}"
        f" [Packages](packages.md){{ .md-button }}"
        f" [Source on GitHub]({REPO}){{ .md-button }}",
        "",
    ]


if __name__ == "__main__":
    main()
