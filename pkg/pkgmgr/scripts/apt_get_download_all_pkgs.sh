if [ "$IGNORE_ERRORS" = "true" ]; then
    set -x
else
    set -ex
fi

mkdir -p /var/cache/apt/archives/partial
# give _apt write access (uid 104 in Debian images – cheaper than chown):
chmod 1777 /var/cache/apt/archives /var/cache/apt/archives/partial

packages="$(tr '\n' ' ' </var/cache/apt/archives/packages.txt)"

APT_OPTS='-o APT::Sandbox::User=root'

apt-get $APT_OPTS update
apt-get $APT_OPTS download --no-install-recommends ${packages}

dpkg --root=/tmp/debian-rootfs \
    --admindir=/tmp/debian-rootfs/var/lib/dpkg \
    --force-all --force-confold --install *.deb
dpkg --root=/tmp/debian-rootfs --configure -a

# create new status.d with contents from status file after updates
STATUS_FILE="/tmp/debian-rootfs/var/lib/dpkg/status"
OUTPUT_DIR="/tmp/debian-rootfs/var/lib/dpkg/status.d"
mkdir -p "$OUTPUT_DIR"

# If no status file is present, synthesise one from status.d or create an empty
if [ ! -f "$STATUS_FILE" ]; then
    if [ -d "$DPKG_DIR/status.d" ] && [ "$(ls -A "$DPKG_DIR/status.d")" ]; then
        # Concatenate all fragment files into a single status file
        cat "$DPKG_DIR"/status.d/* >"$STATUS_FILE"
    else
        # Fallback: empty file so dpkg can initialise
        touch "$STATUS_FILE"
    fi
fi

package_name=""
package_content=""

while IFS= read -r line || [ -n "$line" ]; do
    if [ -z "$line" ]; then
        # end of a package block
        if [ -n "$package_name" ]; then
            # handle special case for base-files
            if [ "$package_name" = "base-files" ]; then
                output_name="base"
            else
                output_name="$package_name"
            fi
            # write the collected content to the package file
            echo "$package_content" >"$OUTPUT_DIR/$output_name"
        fi

        # re-set for next package
        package_name=""
        package_content=""
    else
        # add current line to package content
        if [ -z "$package_content" ]; then
            package_content="$line"
        else
            package_content="$package_content
$line"
        fi

        case "$line" in
        "Package:"*)
            # extract package name
            package_name=$(echo "$line" | cut -d' ' -f2)
            ;;
        esac
    fi
done <"$STATUS_FILE"

# handle last block if file does not end with a newline
if [ -n "$package_name" ] && [ -n "$package_content" ]; then
    echo "$package_content" >"$OUTPUT_DIR/$package_name"
fi

# delete everything else inside /tmp/debian-rootfs/var/lib/dpkg except status.d
find /tmp/debian-rootfs/var/lib/dpkg -mindepth 1 -maxdepth 1 ! -name "status.d" -exec rm -rf {} +

# write results manifest for validation
for deb in *.deb; do
    dpkg-deb -f "$deb" | grep "^Package:\|^Version:" >>/tmp/debian-rootfs/manifest
done

exit 0
