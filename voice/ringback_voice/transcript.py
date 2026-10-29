"""A call's transcript, in the calling session's project, for the session to read.

It's written under `.ringback/calls/` as the call goes (see "Call transcripts" in
docs/design.md). The folder holds its own `.gitignore` of `*`, so git ignores it without a
change to the project's.
"""

from datetime import datetime
from pathlib import Path


class Transcript:
    def __init__(self, project: Path, subject: str, briefing: str):
        calls = project / ".ringback" / "calls"
        calls.mkdir(parents=True, exist_ok=True)
        ignore = project / ".ringback" / ".gitignore"
        if not ignore.exists():
            ignore.write_text("*\n")
        now = datetime.now()
        self.path = calls / f"{now:%Y-%m-%d-%H%M%S}.md"
        self.relative = self.path.relative_to(project).as_posix()
        self.path.write_text(
            f"# Call: {subject or 'no subject'}\n\n{now:%Y-%m-%d %H:%M}\n\n"
            f"## The briefing the voice had\n\n{briefing.strip()}\n\n## The call\n\n"
            "The developer's words are as speech recognition heard them.\n\n"
        )

    def add(self, who: str, text: str) -> None:
        text = text.strip()
        if text:
            with self.path.open("a") as f:
                f.write(f"**{who}:** {text}\n\n")
