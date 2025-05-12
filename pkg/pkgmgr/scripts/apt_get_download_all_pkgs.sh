if [ "$IGNORE_ERRORS" = "true" ]; then
    set -x
else
    set -ex
fi

# --------------------------------------------------------------------
# 0) writable working dir for the .deb files
WORKDIR=/tmp/debs
mkdir -p "$WORKDIR"
cd "$WORKDIR"

# 1) apt sandbox directories so `_apt` (or root) can write
mkdir -p /var/cache/apt/archives/partial
chmod 1777 /var/cache/apt/archives /var/cache/apt/archives/partial

# 2) seed (or create) dpkg database in the *target* rootfs
DPKG_DIR=/tmp/debian-rootfs/var/lib/dpkg
STATUS_FILE="$DPKG_DIR/status"

mkdir -p "$DPKG_DIR"
if [ ! -f "$STATUS_FILE" ]; then
    if [ -d "$DPKG_DIR/status.d" ] && [ "$(ls -A "$DPKG_DIR/status.d")" ]; then
        : >"$STATUS_FILE" # truncate/create
        for f in "$DPKG_DIR"/status.d/*; do
            cat "$f" >>"$STATUS_FILE"
            echo >>"$STATUS_FILE" # always add separator
        done
    else
        : >"$STATUS_FILE" # empty file is enough for dpkg init
    fi
fi

# 3) download the required packages into $WORKDIR
packages="$(tr '\n' ' ' </var/cache/apt/archives/packages.txt)"

APT_OPTS='-o APT::Sandbox::User=root' # stay root
APT_OPTS="$APT_OPTS -o Dir::Cache::Archives=$WORKDIR"
APT_OPTS="$APT_OPTS -o Dir::Cache::Temp=$WORKDIR/partial"

mkdir -p "$WORKDIR/partial"

apt-get $APT_OPTS update
apt-get $APT_OPTS download --no-install-recommends ${packages}

# 4) install them into the extracted rootfs
dpkg --root=/tmp/debian-rootfs \
    --admindir="$DPKG_DIR" \
    --force-all --force-confold --install *.deb
dpkg --root=/tmp/debian-rootfs --configure -a

# 5) rebuild status.d from the updated status file
OUTPUT_DIR="$DPKG_DIR/status.d"
mkdir -p "$OUTPUT_DIR"

awk -v outdir="$OUTPUT_DIR" '
  /^Package:/      { pkg=$2 }
  /^$/ && pkg      {
        outname = (pkg=="base-files") ? "base" : pkg
        print block > outdir "/" outname
        block=""; pkg=""
        next
  }
  { block = block $0 ORS }
  END { if (pkg) { outname = (pkg=="base-files") ? "base" : pkg; print block > outdir "/" outname } }
' "$STATUS_FILE"

# 6) prune everything except status.d
find "$DPKG_DIR" -mindepth 1 -maxdepth 1 ! -name "status.d" -exec rm -rf {} +

# 7) write manifest for CI validation
for deb in *.deb; do
    dpkg-deb -f "$deb" | grep -E '^(Package|Version):' >>/tmp/debian-rootfs/manifest
done

exit 0
