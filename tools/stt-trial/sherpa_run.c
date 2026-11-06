// The words of each stretch, on sherpa-onnx's C API with the settings speech/internal/engine/sherpa.go
// uses, so a sherpa-onnx model runs here as it does on the speech node. Each request on stdin is
// a uint32 count of samples and that many floats at 16 kHz; each answer is one line of text.
//
//   sherpa_run transducer MODEL_DIR ENCODER DECODER JOINER [cuda|cpu] [threads]
//   sherpa_run cohere     MODEL_DIR ENCODER DECODER        [cuda|cpu] [threads]
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "sherpa-onnx/c-api/c-api.h"

static char *join(const char *dir, const char *name) {
  char *p = malloc(strlen(dir) + strlen(name) + 2);
  sprintf(p, "%s/%s", dir, name);
  return p;
}

int main(int argc, char **argv) {
  if (argc < 5) return 2;
  int transducer = strcmp(argv[1], "transducer") == 0;
  const char *dir = argv[2];
  int rest = transducer ? 6 : 5;
  const char *provider = argc > rest ? argv[rest] : "cuda";
  int threads = argc > rest + 1 ? atoi(argv[rest + 1]) : 2;

  SherpaOnnxOfflineRecognizerConfig c;
  memset(&c, 0, sizeof(c));
  c.feat_config.sample_rate = 16000;
  c.feat_config.feature_dim = 80;
  c.model_config.tokens = join(dir, "tokens.txt");
  c.model_config.num_threads = threads;
  c.model_config.provider = provider;
  c.decoding_method = "greedy_search";
  if (transducer) {
    c.model_config.transducer.encoder = join(dir, argv[3]);
    c.model_config.transducer.decoder = join(dir, argv[4]);
    c.model_config.transducer.joiner = join(dir, argv[5]);
    c.model_config.model_type = "nemo_transducer";
  } else {
    c.model_config.cohere_transcribe.encoder = join(dir, argv[3]);
    c.model_config.cohere_transcribe.decoder = join(dir, argv[4]);
    c.model_config.cohere_transcribe.language = "en";
    c.model_config.cohere_transcribe.use_punct = 1;
    c.model_config.cohere_transcribe.use_itn = 1;
  }
  const SherpaOnnxOfflineRecognizer *r = SherpaOnnxCreateOfflineRecognizer(&c);
  if (!r) {
    fprintf(stderr, "the model didn't load\n");
    return 1;
  }
  printf("ready\n");
  fflush(stdout);

  uint32_t n;
  while (fread(&n, 4, 1, stdin) == 1) {
    float *x = malloc(n * sizeof(float));
    if (fread(x, sizeof(float), n, stdin) != n) return 1;
    const SherpaOnnxOfflineStream *s = SherpaOnnxCreateOfflineStream(r);
    SherpaOnnxAcceptWaveformOffline(s, 16000, x, n);
    SherpaOnnxDecodeOfflineStream(r, s);
    const SherpaOnnxOfflineRecognizerResult *res = SherpaOnnxGetOfflineStreamResult(s);
    const char *text = res ? res->text : "";
    for (const char *p = text; *p; p++)
      if (*p != '\n') putchar(*p);
    putchar('\n');
    fflush(stdout);
    if (res) SherpaOnnxDestroyOfflineRecognizerResult(res);
    SherpaOnnxDestroyOfflineStream(s);
    free(x);
  }
  return 0;
}
