#!/bin/sh
# Builds the speech node's install directory on Linux x86-64 with an NVIDIA GPU: sherpa-onnx's
# prebuilt GPU build, the NVIDIA libraries it needs (from their PyPI wheels), ONNX Runtime's
# headers, the models, espeak-ng and, if the machine has no Go, a Go toolchain; then env.sh and
# speech.toml. Next: speech/build.sh DIR, the hub and token in DIR/speech.toml, and run.
# Usage: speech/install.sh [DIR]   (default: ~/ringback-speech)
#
# Each download is checked against its pinned SHA-256, and each part of DIR against the
# SHA-256 of its files (a folder's: of `sha256sum` of its files, sorted by path), pinned from
# the first install, made by hand, so every file is the same as there. A second run hashes
# what's there, keeps what's right and fetches only the rest; a part that differs is replaced,
# and nothing outside DIR is touched. The parts download and unpack side by side; their
# archives stay in DIR/download until the part is in place, so a run that fails or is stopped
# keeps what it fetched for the next one.
# It needs curl or wget, tar, bzip2, gzip, unzip and sha256sum.
set -eu
die() { echo "$*" >&2; exit 1; }
# atleast A B: whether version A is B or later
atleast() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n 1)" = "$2" ]; }

# The wrong machine: sherpa-onnx's GPU build and NVIDIA's wheels are for x86-64 Linux, and
# their CUDA 13 needs a driver of the 580 series or later (13.x minor version compatibility)
# and a GPU of compute capability 7.5 (Turing) or later.
[ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ] ||
    die "This is $(uname -s) on $(uname -m); the speech node's install is for Linux on x86-64."
driver=$(nvidia-smi --query-gpu=driver_version --format=csv,noheader 2>/dev/null | head -n 1) || true
[ -n "$driver" ] || die "No NVIDIA driver: nvidia-smi isn't there or finds no GPU. The speech node
needs an NVIDIA GPU and its driver, 580 or later (for CUDA 13)."
atleast "$driver" 580 || die "The NVIDIA driver is $driver; CUDA 13 needs 580 or later."
cap=$(nvidia-smi --query-gpu=compute_cap --format=csv,noheader 2>/dev/null | head -n 1) || true
[ -z "$cap" ] || atleast "$cap" 7.5 ||
    die "The GPU's compute capability is $cap; CUDA 13 and cuDNN 9 need 7.5 (Turing) or later."
atleast "$driver" 610.43.02 || echo "Note: driver $driver is older than CUDA 13.3's (610.43.02), \
whose libraries these are. CUDA 13's minor version compatibility should cover that, but it's \
untried: if the node can't use the GPU, update the driver."
missing=
for t in tar bzip2 gzip unzip sha256sum; do command -v $t >/dev/null || missing="$missing $t"; done
command -v curl >/dev/null || command -v wget >/dev/null || missing="$missing curl"
[ -z "$missing" ] || die "Missing:$missing."

SRC=$(cd "$(dirname "$0")" && pwd)
DIR=${1:-$HOME/ringback-speech}
# DIR is new, empty or an install directory (which has env.sh, written first), so the parts
# this replaces are never someone else's lib/ or go/.
if [ -d "$DIR" ] && [ -n "$(ls -A "$DIR")" ] && [ ! -e "$DIR/env.sh" ]; then
    die "$DIR isn't empty and has no env.sh, so it isn't a speech node's install directory."
fi
mkdir -p "$DIR"
DIR=$(cd "$DIR" && pwd)
DL="$DIR/download"
start=$(date +%s)
# One run at a time in DIR. The parts run in the background, which ignores Ctrl-C, so the
# trap stops them all (the whole process group) and ends the run.
mkdir "$DIR/.installing" 2>/dev/null ||
    die "install.sh is running in $DIR already (if it was killed, remove $DIR/.installing)."
trap 'rmdir "$DIR/.installing"' EXIT
trap 'trap "" INT TERM; kill 0; exit 130' INT TERM

# env.sh, as the hand-made install's (the same bytes for ~/ringback-speech), and speech.toml;
# neither is written over if it's there.
case $DIR in
"$HOME"/*) home="\$HOME/${DIR#"$HOME"/}" shown="~/${DIR#"$HOME"/}" ;;
*) home=$DIR shown=$DIR ;;
esac
env_sh() {
    cat <<EOF
# Source this: . $shown/env.sh
# sherpa-onnx v2.0.1 (CUDA 13.x / cuDNN 9 / onnxruntime 1.28.2) + NVIDIA CUDA 13.3 runtime libs from PyPI wheels.
export RINGBACK_SPEECH="$home"
export SHERPA_ONNX_DIR="\$RINGBACK_SPEECH/sherpa-onnx"
export LD_LIBRARY_PATH="\$SHERPA_ONNX_DIR/lib:\$RINGBACK_SPEECH/lib\${LD_LIBRARY_PATH:+:\$LD_LIBRARY_PATH}"
export PATH="\$SHERPA_ONNX_DIR/bin:/usr/local/bin:/usr/bin:/bin\${PATH:+:\$PATH}"
EOF
}
if [ ! -e "$DIR/env.sh" ]; then
    env_sh >"$DIR/env.sh"
elif [ "$(env_sh)" != "$(cat "$DIR/env.sh")" ]; then
    echo "Note: $DIR/env.sh isn't the one install.sh writes; it's kept (remove it to have it written)."
fi
if [ ! -e "$DIR/speech.toml" ]; then
    (umask 077 && cp "$SRC/speech.example.toml" "$DIR/speech.toml")
    toml=new
fi

# sums PATH: a file's SHA-256, or a folder's: of `sha256sum` of its files, sorted by path
sums() {
    if [ -d "$1" ]; then
        (cd "$1" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0r sha256sum | sha256sum)
    else
        sha256sum <"$1"
    fi | cut -c1-64
}

# fetch URL SHA256: downloads URL into the part's folder $S, unless it's there already, and
# checks it
fetch() {
    f="$S/${1##*/}"
    [ -f "$f" ] && [ "$(sums "$f")" = "$2" ] && return
    echo "$P: fetching ${1##*/}"
    if command -v curl >/dev/null; then
        curl -fsSL --retry 3 -o "$f.part" "$1"
    else
        wget -q -O "$f.part" "$1"
    fi
    got=$(sums "$f.part")
    [ "$got" = "$2" ] || die "${1##*/}: SHA-256 $got, not the pinned $2"
    mv "$f.part" "$f"
}

# part NAME PATH SHA256: unless DIR/PATH has these sums, the function NAME fetches its
# archives and leaves PATH's files in $S/out (unpacking in $S/x), which, if they have these
# sums, take PATH's place
part() {
    P=$2 S="$DL/$1" t=$(date +%s)
    if [ -e "$DIR/$P" ]; then
        [ "$(sums "$DIR/$P")" != "$3" ] || { echo "$P: there"; return; }
        echo "$P: not the pinned files, so it's replaced"
    fi
    rm -rf "$S/out" "$S/x"
    mkdir -p "$S/x"
    "$1"
    got=$(sums "$S/out")
    [ "$got" = "$3" ] || die "$P: the files unpacked have the SHA-256 $got, not the pinned $3"
    mkdir -p "$(dirname "$DIR/$P")"
    rm -rf "$DIR/$P"
    mv "$S/out" "$DIR/$P"
    rm -rf "$S"
    echo "$P: installed in $(($(date +%s) - t)) s"
}

gh=https://github.com
pypi=https://files.pythonhosted.org/packages

# sherpa-onnx v2.0.1, the CUDA 13 / cuDNN 9 build with ONNX Runtime 1.28.2: lib/, include/
# and five of its tools in bin/ (the ones env.sh's PATH is for)
sherpa() {
    a=sherpa-onnx-v2.0.1-cuda-13.x-cudnn-9.x-onnxruntime1.28.2-linux-x64-gpu
    fetch $gh/k2-fsa/sherpa-onnx/releases/download/v2.0.1/$a.tar.bz2 \
        40ca5c7730a349258f23e9a7bf5c3cbf69f13c498cc50387e8f9f06924a554f8
    tar -xjf "$S/$a.tar.bz2" -C "$S/x" $a/lib $a/include $a/bin/sherpa-onnx-offline \
        $a/bin/sherpa-onnx-offline-tts $a/bin/sherpa-onnx-vad \
        $a/bin/sherpa-onnx-vad-with-offline-asr $a/bin/sherpa-onnx-version
    mv "$S/x/$a" "$S/out"
}

# The NVIDIA libraries ONNX Runtime's CUDA provider loads, from their wheels (CUDA 13.3):
# cudart, cuBLAS and cuBLASLt (not NVBLAS), cuRAND, NVRTC and cuDNN 9. cuFFT and nvJitLink
# are neither linked nor loaded, so they're left out.
nvidia() {
    wheels="$pypi/97/be/5699b6e642b372f7d24c59c2f41383e2696825e20bab85f7399c7c6a56f7/nvidia_cuda_runtime-13.3.29-py3-none-manylinux2014_x86_64.manylinux_2_17_x86_64.whl e04420616e72f563167a7733272992d7e6df6dc5cb54b2f94f9f1520ea9e30c1 nvidia/cu13/lib/libcudart.so.13
$pypi/ce/0d/cc77458e8fb0634597e3994650c2853ee785f2fc61bf370bbb304021cca1/nvidia_cublas-13.5.1.27-py3-none-manylinux_2_27_x86_64.whl db3a1f0c8bc24945a4d195ba199d83f27e650ffdfc0c32e6b8362c314f14d553 nvidia/cu13/lib/libcublas.so.13 nvidia/cu13/lib/libcublasLt.so.13
$pypi/ee/49/4ca4ce4a9334c9a1ef68ab85b358f019bf85e8c3f51c2471d4cc257a273d/nvidia_curand-10.4.3.29-py3-none-manylinux_2_27_x86_64.whl 1859bf37a62754d2c65001393096ca79de399f995971fa7826d0adfd88c3cf7b nvidia/cu13/lib/libcurand.so.10
$pypi/8b/2c/86916c8a34dcdb0c3ddd1c0e30545041bd781184e437b9cb76fcda70560b/nvidia_cuda_nvrtc-13.3.33-py3-none-manylinux2010_x86_64.manylinux_2_12_x86_64.whl 82530788b8c6164a54d3fd9ae8bcca8893d397c4aeb998861982a03bbe41e204 nvidia/cu13/lib/libnvrtc.so.13 nvidia/cu13/lib/libnvrtc-builtins.so.13.3
$pypi/af/75/96ea5c5368eb595c39d629cde08a66227a864e71ccb0e593add2612bb952/nvidia_cudnn_cu13-9.27.0.42-py3-none-manylinux_2_27_x86_64.whl 9677e76f21862eb5da7ee5ed69d544738b2d8b5c3ce7e5ec125c5592e6cdbdc8 nvidia/cudnn/lib/*"
    # PyPI is slow on one connection (8 MB/s, measured), so the wheels come side by side.
    pids=
    while read -r url sum files; do
        fetch "$url" "$sum" &
        pids="$pids $!"
    done <<EOF
$wheels
EOF
    ok=1
    for p in $pids; do wait "$p" || ok=; done
    [ -n "$ok" ] || die "lib: a wheel didn't come"
    set -f
    while read -r url sum files; do
        unzip -q -j "$S/${url##*/}" $files -d "$S/x"
    done <<EOF
$wheels
EOF
    set +f
    mv "$S/x" "$S/out"
}

# ONNX Runtime 1.28.2's headers (the node calls its C API for Silero and Smart Turn) and licence
onnxruntime() {
    a=onnxruntime-linux-x64-1.28.2
    fetch $gh/microsoft/onnxruntime/releases/download/v1.28.2/$a.tgz \
        d7209b8751b27b862b0c76332c2e20e203396edb5dab700ecf4bb485cf147415
    tar -xzf "$S/$a.tgz" -C "$S/x" $a/LICENSE $a/include
    mv "$S/x/$a" "$S/out"
}

# Silero VAD v5 and Smart Turn v3.2: the files Pipecat 1.14.0 ships, as the Python voice uses
pipecat=$pypi/38/31/29c72ca8d3a16b59fd12580fcb99a7cef641d9ee07fe7b0321c1b65ca3cf/pipecat_ai-1.14.0-py3-none-any.whl
pipecat_sum=0352f504b44e78bdf524e45af4bc759601c835b9b7dc195efe5e03384b974a44
silero() {
    fetch $pipecat $pipecat_sum
    unzip -q -j "$S/${pipecat##*/}" pipecat/audio/vad/data/silero_vad.onnx -d "$S/x"
    mv "$S/x/silero_vad.onnx" "$S/out"
}
smart_turn() {
    fetch $pipecat $pipecat_sum
    unzip -q -j "$S/${pipecat##*/}" pipecat/audio/turn/smart_turn/data/smart-turn-v3.2-cpu.onnx -d "$S/x"
    mv "$S/x/smart-turn-v3.2-cpu.onnx" "$S/out"
}

# Parakeet TDT 0.6B v2 in fp16 (for a GPU) and Kokoro multi-lang v1.0, from sherpa-onnx's
# releases of models
parakeet() {
    a=sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16
    fetch $gh/k2-fsa/sherpa-onnx/releases/download/asr-models/$a.tar.bz2 \
        37f67a1a6c942dae27d345ee395fbd19e25ee48996faf70fca25779026054cf0
    tar -xjf "$S/$a.tar.bz2" -C "$S/x"
    mv "$S/x/$a" "$S/out"
}
kokoro() {
    a=kokoro-multi-lang-v1_0
    fetch $gh/k2-fsa/sherpa-onnx/releases/download/tts-models/$a.tar.bz2 \
        60703707bc05ccd48d60e1eb723f38b07d6e474795a0b6a588d7220400e0f7a4
    tar -xjf "$S/$a.tar.bz2" -C "$S/x"
    mv "$S/x/$a" "$S/out"
}

# espeak-ng 1.52 from the espeakng-loader 0.2.4 wheel, as the Python voice's misaki: the
# library and the data English needs (en_dict, phon*, intonations, lang/, voices/)
espeak() {
    w=$pypi/de/1e/25ec5ab07528c0fbb215a61800a38eca05c8a99445515a02d7fa5debcb32/espeakng_loader-0.2.4-py3-none-manylinux_2_17_x86_64.manylinux2014_x86_64.whl
    fetch $w 08721baf27d13d461f6be6eed9a65277e70d68234ff484fd8b9897b222cdcb6d
    d=espeakng_loader/espeak-ng-data
    unzip -q "$S/${w##*/}" espeakng_loader/libespeak-ng.so.1 "$d/en_dict" "$d/phon*" \
        "$d/intonations" "$d/lang/*" "$d/voices/*" -d "$S/x"
    mkdir -p "$S/out/lib"
    mv "$S/x/espeakng_loader/libespeak-ng.so.1" "$S/out/lib"
    mv "$S/x/$d" "$S/out"
}

# Go 1.27.3, go.mod's version, which build.sh uses from DIR/go when it's there
golang() {
    fetch https://go.dev/dl/go1.27.3.linux-amd64.tar.gz \
        65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40
    tar -xzf "$S/go1.27.3.linux-amd64.tar.gz" -C "$S/x"
    mv "$S/x/go" "$S/out"
}

# Go only if there's none on PATH as recent as go.mod asks (build.sh builds with GOTOOLCHAIN=local)
need=$(sed -n 's/^go //p' "$SRC/go.mod")
have=$(go env GOVERSION 2>/dev/null | sed 's/^go//') || true
path_go=
if [ -n "$have" ] && atleast "$have" "$need"; then
    echo "go: not needed, Go $have is on PATH"
    path_go=1
elif [ -n "$have" ]; then
    echo "go: Go $have on PATH is older than go.mod's $need, so DIR/go gets one"
fi

pids=
run() {
    (part "$@") &
    pids="$pids $!"
}
[ -n "$path_go" ] || run golang go a8550277699804dbd67c7b3c30597e15390fd510a63aef4e63d885a8ca2a4992
run sherpa sherpa-onnx 1743ff6b6ec6cd5e5c77441352a19c88dc6d7ff8e5203837bf82c7b077ffbc78
run nvidia lib 94be81f4c3ef6781fa85e1486441b93e321a2985741a3c31204f64380eca23bd
run onnxruntime onnxruntime 37dd398c4a0b5e5fd358cb422d7005397f50095ba86f0cb2292e2971a98ff8ce
run silero models/silero-vad-v5.onnx 1a153a22f4509e292a94e67d6f9b85e8deb25b4988682b7e174c65279d8788e3
run smart_turn models/smart-turn-v3.2-cpu.onnx 2bb026316b14a660486a75b1733cd3fbab8c2fd0314dc9af7be49f8cca967e4f
run parakeet models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16 d6d81613333b9ffce59a0aa8b23e6cb73fc9b13ec17c71ba5be235bd1a17b5e3
run kokoro models/kokoro-multi-lang-v1_0 c09c6b5d6a5b3ef3ad821763864f5ef74abbf36486555836dc7311dd010eebac
run espeak espeak-ng 896ed05b4121b88550415cc5340e70c3e77d871c09d846bebb726dacc3a7b74e
ok=1
for p in $pids; do wait "$p" || ok=; done
[ -n "$ok" ] || die "Some parts failed (above). What was fetched is in $DL; run install.sh again."
rm -rf "$DL"

echo "$DIR is ready, in $(($(date +%s) - start)) s. Next:"
echo "  $SRC/build.sh $DIR"
[ "${toml:-}" != new ] || echo "  set hub and token in $DIR/speech.toml (from speech.example.toml)"
echo "  . $DIR/env.sh && $DIR/bin/ringback-speech serve"
