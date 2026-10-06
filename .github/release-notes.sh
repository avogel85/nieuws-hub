#!/bin/sh
# Prints the release notes for a version: its "### X.Y.Z" entry from the README changelog,
# followed by the install/update instructions. Used by .github/workflows/release.yml;
# run it locally to preview:  sh .github/release-notes.sh 1.7.2
set -eu
v="$1"
notes="$(awk -v h="### $v" '$0 == h { f = 1; next } f && (/^### / || /^## / || /^---/) { exit } f' README.md)"
if [ -z "$(printf '%s' "$notes" | tr -d ' \n')" ]; then
  echo "no changelog entry '### $v' in README.md" >&2
  exit 1
fi
minor="${v%.*}"
major="${v%%.*}"
printf '%s\n' "$notes"
cat <<NOTES

---

## Install or update

**Docker, ready-made image** (linux/amd64, linux/arm64):
\`\`\`sh
git pull
docker compose pull && docker compose up -d
\`\`\`
The image is \`ghcr.io/digibaro/news-dashboard-docker:$v\`; it is also tagged \`:$minor\`, \`:$major\` and \`:latest\`. \`NDB_TAG\` in \`.env\` chooses which one you follow (default \`$major\`).

**Docker, built on your server** from your checkout (the tests run during the build):
\`\`\`sh
git pull
docker compose build && docker compose up -d
\`\`\`

**Without Docker (systemd):** download the archive for your platform below, replace the program, then run \`sudo systemctl restart nieuwsdashboard\`.

Your own \`config.yaml\`, \`.env\` and compose files are not changed by an update. To pick up new options, compare them with \`config.yaml.default\` and \`docker-compose.yml.default\`.

Verify downloads with \`sha256sum -c SHA256SUMS\`. Licensed under the GNU GPL v3.0.
NOTES
