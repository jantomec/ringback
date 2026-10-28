"""Three replies the voice might speak, from one sentence to a long answer."""

TEXTS = {
    "short": "All forty tests pass, and the branch is ready to merge.",
    "medium": (
        "I moved the switchboard to the always-on Mac. It starts on its own after a restart, "
        "and it logs to a file in your home folder. The phone still points at the old address, "
        "so you'll need to change it in the app."
    ),
    "long": (
        "I finished the speech-to-text service. It loads Parakeet once when the voice server "
        "starts, so the first call doesn't wait for it. Each of your turns takes about forty "
        "milliseconds to transcribe. I haven't tested it with background noise yet, and the "
        "interruption handling still needs the voice filter. Do you want me to start on that "
        "next, or should I test the latency on the phone first?"
    ),
}
RUNS = 5  # timed runs per text, after one warm-up
