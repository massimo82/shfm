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

//go:build vault

package vault

import (
	"fmt"
	"strings"
)

// recoveryText is RECOVERY.txt: how to get the files back without shfm,
// with the age tools only (plus jq for encrypted names).
func recoveryText(cfg config) string {
	var b strings.Builder
	b.WriteString(`ENCRYPTED VAULT — HOW TO RECOVER THE FILES WITHOUT SHFM
=======================================================

This folder is an encrypted vault made by shfm (https://github.com/massimo82/shfm).
Every file in it is a standard age file (https://age-encryption.org):
it can be decrypted with the age command line tool, or with rage.

You need ONE of:
  - the vault's password, which protects identity.age;
  - the recovery key shown when the vault was created (a line starting
    with AGE-SECRET-KEY-), saved in a file, e.g. key.txt.

1. Get the key file
-------------------
With the password:

    age -d -o key.txt identity.age        # asks for the password

With the recovery key: write it on a line of its own in key.txt.

Keep key.txt private, and delete it when done.
`)
	if strings.HasPrefix(cfg.Recipient, "age1pq1") {
		b.WriteString(`
This vault uses a post-quantum key (AGE-SECRET-KEY-PQ-1...): age 1.3.0 or
later reads it as is. With rage, or an older age, install age-plugin-pq
(it comes with age 1.3.0 and later) and convert the key first, then use
plugin-key.txt instead of key.txt below:

    age-plugin-pq -identity -o plugin-key.txt key.txt
`)
	}
	if cfg.Names == NamesPlain {
		b.WriteString(`
2. Decrypt the files
--------------------
File and folder names are visible: every file is "<name>.age". One file:

    age -d -i key.txt -o "photo.jpg" "photo.jpg.age"

Every file, in place (from this folder; identity.age is the key, skipped):

    find . -name '*.age' ! -path ./identity.age -exec sh -c \
      'age -d -i key.txt -o "${1%.age}" "$1"' sh {} \;
`)
		return b.String()
	}
	b.WriteString(`
2. Find the names
-----------------
Names are encrypted: on disk every file is "<ID>.age" and every folder
"<ID>". Each folder's .index.age maps the IDs to the real names:

    age -d -i key.txt .index.age | jq .

("dir": true marks a folder; .index.age.bak is the previous version of the
index, .index.age.tmp a newer one an interruption may have left.)

3. Decrypt the files
--------------------
One file:

    age -d -i key.txt -o "photo.jpg" "<ID>.age"

The whole vault into a folder, with this POSIX shell script (needs age and
jq; run it from this folder, names containing a line break excepted):

    KEY=key.txt
    restore() {
      mkdir -p "$2"
      age -d -i "$KEY" "$1/.index.age" |
        jq -r '.entries[] | "\(.id) \(if .dir then "d" else "f" end) \(.name)"' |
        while IFS=' ' read -r id kind name; do
          if [ "$kind" = d ]; then
            restore "$1/$id" "$2/$name"
          else
            age -d -i "$KEY" -o "$2/$name" "$1/$id.age"
          fi
        done
    }
    restore . ../recovered
`)
	return b.String()
}

// splitRecoveryText is the RECOVERY.txt written, in clear, at the root of
// each part of a split vault: how to put the vault back together from any
// two parts, before following the vault's own RECOVERY.txt.
func splitRecoveryText(part int) string {
	return fmt.Sprintf(`SPLIT ENCRYPTED VAULT — HOW TO RECOVER IT WITHOUT SHFM
=====================================================

This folder is part %d of 3 of a split encrypted vault made by shfm
(https://github.com/massimo82/shfm). Every file of the vault is stored in
three shards, one on each part:

  <name>.<gen>.a    on part 1: the first half of the file
  <name>.<gen>.b    on part 2: the second half
  <name>.<gen>.c0   on part 3: the first half XOR the second (c1: the
                    second half was one byte shorter, padded with a zero)

Any two parts are enough. With parts 1 and 2, a file is simply

    cat NAME.GEN.a NAME.GEN.b > NAME

(GEN must be the same for both: when several are found, take the one
present on two parts). Folders are the same on every part.

To put the whole vault back together from any two parts, save the Python 3
script below as reassemble.py and run it with the parts' folders, "-" for
the missing one:

    python3 reassemble.py PART1 PART2 PART3 vault

The "vault" folder is then a regular encrypted vault: follow the
RECOVERY.txt in it, which needs the vault's password or recovery key.

---- reassemble.py ----
%s---- end ----
`, part+1, reassembleScript)
}

const reassembleScript = `import os, re, sys

parts = [None if p == "-" else p for p in sys.argv[1:4]]
dest = sys.argv[4]
shard = re.compile(r"^(.+)\.([0-9a-f]{8})\.(a|b|c0|c1)$")
part_of = {"a": 0, "b": 1, "c0": 2, "c1": 2}
CHUNK = 1 << 20

def copy(src, n, out):
    with open(src, "rb") as f:
        while n > 0:
            data = f.read(min(CHUNK, n))
            if not data:
                sys.exit("truncated shard: " + src)
            out.write(data)
            n -= len(data)

def xor(src1, src2, n, out):
    # n bytes of src1 XOR src2, a shorter src2 counting as zeros
    with open(src1, "rb") as f1, open(src2, "rb") as f2:
        while n > 0:
            a = f1.read(min(CHUNK, n))
            if not a:
                sys.exit("truncated shard: " + src1)
            b = f2.read(len(a)).ljust(len(a), b"\0")
            out.write(bytes(x ^ y for x, y in zip(a, b)))
            n -= len(a)

def restore(rel):
    dirs = [os.path.join(p, rel) if p else None for p in parts]
    os.makedirs(os.path.join(dest, rel), exist_ok=True)
    files, subdirs = {}, set()
    for i, d in enumerate(dirs):
        if not d or not os.path.isdir(d):
            continue
        for n in os.listdir(d):
            if os.path.isdir(os.path.join(d, n)):
                subdirs.add(n)
                continue
            m = shard.match(n)
            if m and part_of[m.group(3)] == i:
                files.setdefault(m.group(1), {}).setdefault(m.group(2), {})[i] = os.path.join(d, n)
    for name, gens in files.items():
        have = max(gens.values(), key=len)  # the generation on most parts
        if len(have) < 2:
            print("lost (a single shard):", os.path.join(rel, name))
            continue
        size = {i: os.path.getsize(p) for i, p in have.items()}
        pad = int(have[2][-1]) if 2 in have else size[0] - size[1]
        len_a = size[0] if 0 in have else size[2]
        len_b = size[1] if 1 in have else len_a - pad
        with open(os.path.join(dest, rel, name), "wb") as out:
            if 0 in have:
                copy(have[0], len_a, out)
            else:
                xor(have[2], have[1], len_a, out)
            if 1 in have:
                copy(have[1], len_b, out)
            else:
                xor(have[0], have[2], len_b, out)
    for s in subdirs:
        restore(os.path.join(rel, s))

restore("")
`
