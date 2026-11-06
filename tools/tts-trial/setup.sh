#!/bin/sh
# Sets up the text-to-speech trial on the speech node, all of it under one folder. Nothing is
# installed system-wide and nothing needs root. Run it again and it fetches only what's missing.
# Usage: tools/tts-trial/setup.sh [ROOT]   (default: ~/ringback-models-test/tts)
set -e
ROOT=${1:-$HOME/ringback-models-test/tts}
AUDIOCPP=v0.9.4
BREEZE=v0.1.6
PTTS=0.6.0
UV=0.13.7
mkdir -p "$ROOT"/bin "$ROOT"/runtime "$ROOT"/models "$ROOT"/download
cd "$ROOT"
get() { # URL FILE: into download/, resuming
    [ -s "download/$2.done" ] || { curl -fL --retry 3 -C - -o "download/$2" "$1" && echo ok > "download/$2.done"; }
}
gh=https://github.com

# uv, for Python and the scoring environment
if [ ! -x bin/uv ]; then
    get $gh/astral-sh/uv/releases/download/$UV/uv-x86_64-unknown-linux-gnu.tar.gz uv.tar.gz
    mkdir -p runtime/uv && tar -xzf download/uv.tar.gz -C runtime/uv --strip-components=1
    cp runtime/uv/uv bin/uv
fi

# audio.cpp: Ming, Breeze, Audio8 and the others, on CUDA, with its own CUDA libraries
a=audio-$AUDIOCPP-bin-ubuntu-x64-cuda12.8-colab
r=audio-$AUDIOCPP-cudart-ubuntu-x64-cuda12.8
if [ ! -d runtime/audiocpp ]; then
    get $gh/0xShug0/audio.cpp/releases/download/$AUDIOCPP/$a.tar.gz $a.tar.gz
    get $gh/0xShug0/audio.cpp/releases/download/$AUDIOCPP/$r.tar.gz $r.tar.gz
    mkdir -p runtime/audiocpp.new/bin runtime/audiocpp.new/cudart
    tar -xzf download/$a.tar.gz -C runtime/audiocpp.new/bin
    tar -xzf download/$r.tar.gz -C runtime/audiocpp.new/cudart
    mv runtime/audiocpp.new runtime/audiocpp
fi
# Its CUDA build also links NCCL, which its runtime archive leaves out: NVIDIA's wheel has it.
if [ ! -e runtime/audiocpp/cudart/libnccl.so.2 ]; then
    w=nvidia_nccl_cu12-2.32.3-py3-none-manylinux_2_27_x86_64.whl
    get https://files.pythonhosted.org/packages/f3/f8/9dce3698eed6fae28078eb38f748feb1665767693d8017c099de9fbf0b08/$w $w
    python3 -c "
import sys, zipfile
z = zipfile.ZipFile(sys.argv[1])
open(sys.argv[2], 'wb').write(z.read('nvidia/nccl/lib/libnccl.so.2'))" download/$w runtime/audiocpp/cudart/libnccl.so.2
    rm -f download/$w
fi

# The Breeze port's own runtime (Vulkan)
a=breeze-tts-2-$BREEZE-linux-x64-vulkan
if [ ! -d runtime/breeze ]; then
    get $gh/HoppouAI/Breeze-TTS-2.cpp/releases/download/$BREEZE/$a.tar.gz $a.tar.gz
    mkdir -p runtime/breeze.new && tar -xzf download/$a.tar.gz -C runtime/breeze.new
    mv runtime/breeze.new runtime/breeze
fi

# Phonon's own runtime (CPU)
a=ptts-$PTTS-x86_64-unknown-linux-gnu
if [ ! -d runtime/ptts ]; then
    get $gh/gradium-ai/xn-ptts/releases/download/v$PTTS/$a.tar.gz $a.tar.gz
    mkdir -p runtime/ptts.new && tar -xzf download/$a.tar.gz -C runtime/ptts.new
    mv runtime/ptts.new runtime/ptts
fi
echo "runtimes in $ROOT/runtime"
du -sh runtime/* bin

# The models. Each is one file from Hugging Face; a second run skips what's there.
hf() { # REPO PATH-IN-REPO FILE-UNDER-models/
    [ -s "models/$3.done" ] && return 0
    mkdir -p "$(dirname "models/$3")"
    curl -fL --retry 3 -C - -o "models/$3" "https://huggingface.co/$1/resolve/main/$2"
    echo ok > "models/$3.done"
}
g=audio-cpp/audio.cpp-gguf
hf audio-cpp/Ming-3.0-TTS-0.5B-GGUF ming-3.0-tts-0.5b-q8_0.gguf ming/ming-3.0-tts-0.5b-q8_0.gguf
hf audio-cpp/Audio8-TTS-Realistic-0.6b-GGUF audio8-tts-realistic-0.6b-q8_0.gguf audio8/audio8-tts-realistic-0.6b-q8_0.gguf
hf LiquidAI/LFM3-Audio-2B-GGUF LFM3-Audio-2B-Q8_0.gguf lfm3/LFM3-Audio-2B-Q8_0.gguf
hf LiquidAI/LFM3-Audio-2B-GGUF mmproj-LFM3-Audio-2B-Q8_0.gguf lfm3/mmproj-LFM3-Audio-2B-Q8_0.gguf
hf $g Qwen3-TTS-12Hz-1.7B-CustomVoice-GGUF/qwen3-tts-12hz-1.7b-customvoice-q8_0.gguf qwen3/qwen3-tts-12hz-1.7b-customvoice-q8_0.gguf
hf $g VoxCPM2-GGUF/voxcpm2-q8_0.gguf voxcpm2/voxcpm2-q8_0.gguf
hf $g KugelAudio-0-Open-GGUF/kugelaudio-0-open-q4_k.gguf kugel/kugelaudio-0-open-q4_k.gguf
hf $g Breeze-TTS-2-GGUF/breeze-tts-2-q8_0.gguf breeze/audiocpp/breeze-tts-2-q8_0.gguf
hf HoppouAI/Breeze-TTS-2.cpp breeze-tts-2-q8_0.gguf breeze/port/breeze-tts-2-q8_0.gguf
hf HoppouAI/Breeze-TTS-2.cpp breeze-tts-2-q4_k.gguf breeze/port/breeze-tts-2-q4_k.gguf
echo "models in $ROOT/models"
du -sh models

# The baseline: Kokoro as a call makes it. A second copy of the speech node's libraries, made with
# speech/install.sh less the models it doesn't need; Kokoro's folder is read from the live node,
# and nothing else of the live node is used. SPEECH_SRC is a copy of the repo's speech/ on this
# machine, and the Go program is built inside it (see kokoro_trial.go.txt).
SPEECH_SRC=${SPEECH_SRC:-$ROOT/harness/speech}
KDIR=$ROOT/runtime/kokoro
if [ -d "$SPEECH_SRC" ] && [ ! -x "$KDIR/bin/kokoro-trial" ]; then
    sed '/^run parakeet/d;/^run kokoro/d;/^run silero/d;/^run smart_turn/d' "$SPEECH_SRC/install.sh" \
        > "$SPEECH_SRC/install-trial.sh"
    sh "$SPEECH_SRC/install-trial.sh" "$KDIR"
    mkdir -p "$SPEECH_SRC/cmd/kokoro-trial" "$KDIR/bin"
    cp "$ROOT/harness/kokoro_trial.go.txt" "$SPEECH_SRC/cmd/kokoro-trial/main.go"
    ( cd "$SPEECH_SRC"
      export GOTOOLCHAIN=local GOPATH="$KDIR/gopath" GOCACHE="$KDIR/gocache" CGO_ENABLED=1
      export CGO_CFLAGS="-O2 -I$KDIR/sherpa-onnx/include -I$KDIR/onnxruntime/include"
      export CGO_LDFLAGS="-L$KDIR/sherpa-onnx/lib -Wl,-rpath,\$ORIGIN/../sherpa-onnx/lib"
      GO=go; [ -x "$KDIR/go/bin/go" ] && GO="$KDIR/go/bin/go"
      "$GO" build -o "$KDIR/bin/kokoro-trial" ./cmd/kokoro-trial )
fi

# Python 3.12 and the environment that scores the clips and runs Phonon
export UV_PYTHON_INSTALL_DIR=$ROOT/python UV_CACHE_DIR=$ROOT/cache/uv
if [ ! -x env/bin/python ]; then
    bin/uv venv --python 3.12 env
    bin/uv pip install --python env/bin/python numpy==2.3.5 soundfile==0.13.1 soxr==1.0.0 \
        sherpa-onnx==1.13.2 sherpa-onnx-core==1.13.2 ptts==0.6.0
fi

# The speaker model score.py compares sentences with, and the loudness measure
mkdir -p models/speaker
for s in 3dspeaker_speech_campplus_sv_en_voxceleb_16k.onnx nemo_en_titanet_large.onnx \
    wespeaker_en_voxceleb_resnet34_LM.onnx; do
    [ -s models/speaker/$s ] || curl -fL --retry 3 -o models/speaker/$s \
        $gh/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/$s
done
bin/uv pip install --python env/bin/python pyloudnorm==0.2.0

# Breeze's saved voice, from the reference clip refs.py picked (see README.md). It needs the GPU.
if [ -s refs/breeze.wav ] && [ ! -s refs/breeze-voices/trial.breeze ]; then
    d=$(ls -d runtime/breeze/breeze-tts-2-*)
    mkdir -p refs/breeze-voices
    ( cd refs/breeze-voices
      LD_LIBRARY_PATH="$ROOT/$d" sh "$ROOT/harness/gate.sh" "$ROOT/$d/breeze-cli" \
          "$ROOT/models/breeze/port/breeze-tts-2-q8_0.gguf" --ref-audio "$ROOT/refs/breeze.wav" \
          --ref-text "$(cat "$ROOT/refs/breeze.txt")" --save-voice trial )
    mv refs/breeze-voices/voices/trial.breeze refs/breeze-voices/ 2>/dev/null || true
fi
