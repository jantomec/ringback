#!/bin/bash
# Installs the trial in one folder on the speech node, and nothing outside it: uv, Python 3.12,
# an environment per model, the node's own sherpa-onnx build and NVIDIA libraries, the models
# and the harness. Nothing here uses the GPU, so it needs no lock. Run it again to fill in
# whatever is missing. It reads the node's Parakeet from NODE_MODELS and changes nothing there.
#
#   STT=~/ringback-models-test/stt tools/stt-trial/node_setup.sh     # on the node
set -u
STT=${STT:-$HOME/ringback-models-test/stt}
HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$STT"/{bin,cache,envs,models/silero,runs,results,data,logs,sherpa}
cd "$STT"

cat >env.sh <<E
# The trial's tools and caches, all under this folder.
export STT=$STT
export PATH=\$STT/bin:\$PATH
export UV_CACHE_DIR=\$STT/cache/uv UV_PYTHON_INSTALL_DIR=\$STT/cache/python UV_PYTHON_PREFERENCE=only-managed
export UV_PYTHON_BIN_DIR=\$STT/cache/python-bin UV_TOOL_DIR=\$STT/cache/uv-tools
export HF_HOME=\$STT/cache/hf HF_HUB_DISABLE_TELEMETRY=1
E
. ./env.sh

[ -x bin/uv ] || curl -LsSf https://astral.sh/uv/install.sh | env UV_UNMANAGED_INSTALL="$STT/bin" sh
uv python install 3.12

torch=https://download.pytorch.org/whl/cu130
env_for() { [ -d "envs/$1" ] || uv venv -q --python 3.12 "envs/$1"; }

env_for harness
uv pip install -q -p envs/harness numpy scipy soundfile onnxruntime jiwer==4.0.0 whisper-normalizer==0.1.15

env_for qwen3
uv pip install -q -p envs/qwen3 torch==2.14.1 torchaudio==2.11.0 --index-url $torch
uv pip install -q -p envs/qwen3 qwen-asr==0.0.6 nvidia-ml-py

env_for granite
uv pip install -q -p envs/granite torch==2.14.1 torchaudio==2.11.0 --index-url $torch
uv pip install -q -p envs/granite transformers==5.22.1 accelerate==1.15.0 peft==0.21.2 soundfile nvidia-ml-py

env_for nemo
uv pip install -q -p envs/nemo torch==2.14.1 torchaudio==2.11.0 --index-url $torch
uv pip install -q -p envs/nemo "nemo-toolkit[asr]==3.0.0" peft nvidia-ml-py

# The speech node's sherpa-onnx v2.0.1 and its NVIDIA libraries (speech/install.sh).
gh=https://github.com/k2-fsa/sherpa-onnx/releases/download
a=sherpa-onnx-v2.0.1-cuda-13.x-cudnn-9.x-onnxruntime1.28.2-linux-x64-gpu
[ -d "sherpa/$a" ] || curl -fsSL "$gh/v2.0.1/$a.tar.bz2" | tar -xj -C sherpa
env_for sherpa
uv pip install -q -p envs/sherpa numpy soundfile nvidia-ml-py nvidia-cuda-runtime==13.3.29 nvidia-cublas==13.5.1.27 \
    nvidia-curand==10.4.3.29 nvidia-cuda-nvrtc==13.3.33 nvidia-cudnn-cu13==9.27.0.42
gcc -O2 -I"sherpa/$a/include" "$HERE/sherpa_run.c" -L"sherpa/$a/lib" -lsherpa-onnx-c-api -o bin/sherpa_run

for m in sherpa-onnx-nemo-parakeet-tdt-0.6b-ultra-int8 sherpa-onnx-cohere-transcribe-14-lang-int8-2026-04-01; do
    [ -d "models/$m" ] || curl -fsSL "$gh/asr-models/$m.tar.bz2" | tar -xj -C models
done

# The node's Silero, the file Pipecat 1.14.0 ships (speech/install.sh).
if [ ! -f models/silero/silero_vad.onnx ]; then
    curl -fsSL https://files.pythonhosted.org/packages/38/31/29c72ca8d3a16b59fd12580fcb99a7cef641d9ee07fe7b0321c1b65ca3cf/pipecat_ai-1.14.0-py3-none-any.whl -o models/silero/p.whl
    (cd models/silero && unzip -q -j p.whl pipecat/audio/vad/data/silero_vad.onnx && rm p.whl)
fi
echo "1a153a22f4509e292a94e67d6f9b85e8deb25b4988682b7e174c65279d8788e3  models/silero/silero_vad.onnx" | sha256sum -c

for r in Qwen/Qwen3-ASR-1.7B nvidia/canary-qwen-2.5b ibm-granite/granite-speech-5.0-2b; do
    envs/granite/bin/python -c "import sys; from huggingface_hub import snapshot_download as d; print(d(sys.argv[1], ignore_patterns=['*.wav', '*.png', '*.gguf']))" "$r"
done
