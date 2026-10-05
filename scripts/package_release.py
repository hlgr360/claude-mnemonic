#!/usr/bin/env python3
"""Pack a staged release tree into the archive the updater and the install scripts expect.

Usage: package_release.py <stage-dir> <archive-path>

The format follows the archive name: .zip (Windows) or .mcpb (a Claude Desktop extension is a zip), or .tar.gz. Entries are sorted and carry no owner and a fixed
time, so packing the same tree twice gives the same bytes, and files keep their executable bit. This replaces
GoReleaser's archiver so the release can be built with plain tools on each native runner.
"""
import gzip
import os
import sys
import tarfile
import zipfile

FIXED_TIME = 0


def entries(stage):
    """Relative paths of every file under stage, sorted, with '/' separators."""
    found = []
    for root, _dirs, files in os.walk(stage):
        for name in files:
            found.append(os.path.relpath(os.path.join(root, name), stage).replace(os.sep, "/"))
    return sorted(found)


def pack_tar_gz(stage, archive):
    # A gzip header with a zero mtime and no file name keeps the bytes the same from run to run.
    with open(archive, "wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=FIXED_TIME, compresslevel=9) as gz:
            with tarfile.open(fileobj=gz, mode="w", format=tarfile.PAX_FORMAT) as tar:
                for rel in entries(stage):
                    path = os.path.join(stage, rel)
                    info = tar.gettarinfo(path, arcname=rel)
                    info.uid = info.gid = 0
                    info.uname = info.gname = ""
                    info.mtime = FIXED_TIME
                    info.mode = 0o755 if os.access(path, os.X_OK) else 0o644
                    with open(path, "rb") as f:
                        tar.addfile(info, f)


def pack_zip(stage, archive):
    with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
        for rel in entries(stage):
            info = zipfile.ZipInfo(rel, date_time=(1980, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = (0o755 if os.access(os.path.join(stage, rel), os.X_OK) else 0o644) << 16
            with open(os.path.join(stage, rel), "rb") as f:
                z.writestr(info, f.read())


def pack(stage, archive):
    if not os.path.isdir(stage):
        raise SystemExit(f"not a directory: {stage}")
    if not entries(stage):
        raise SystemExit(f"nothing to pack in {stage}")
    os.makedirs(os.path.dirname(os.path.abspath(archive)), exist_ok=True)
    if archive.endswith((".zip", ".mcpb")):
        pack_zip(stage, archive)
    elif archive.endswith(".tar.gz"):
        pack_tar_gz(stage, archive)
    else:
        raise SystemExit("archive must end in .tar.gz or .zip")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit(__doc__)
    pack(sys.argv[1], sys.argv[2])
