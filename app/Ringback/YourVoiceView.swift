import SwiftUI

/// The guided recording of your voice, opened from the main screen.
struct YourVoiceView: View {
    let voice: YourVoice

    var body: some View {
        Form {
            Section {
                Text(YourVoice.script)
                    .font(.title3)
                    .padding(.vertical, 6)
            } header: {
                Text("Read this aloud")
            } footer: {
                Text("Use the microphone you take calls with, such as your AirPods, somewhere quiet. Read at your usual pace.")
            }
            Section {
                if voice.isRecording {
                    HStack(spacing: 12) {
                        Image(systemName: "waveform").foregroundStyle(.red)
                        ProgressView(value: voice.level).tint(.red)
                        Text(Duration.seconds(voice.elapsed), format: .time(pattern: .minuteSecond))
                            .monospacedDigit()
                            .foregroundStyle(.secondary)
                    }
                    Button("Stop", action: voice.stopRecording)
                } else {
                    Button(voice.recordedAt == nil ? "Record" : "Record Again", action: voice.startRecording)
                    if voice.recordedAt != nil {
                        Button(voice.isPlaying ? "Stop Playing" : "Play") {
                            voice.isPlaying ? voice.stopPlaying() : voice.play()
                        }
                    }
                }
            } footer: {
                if let error = voice.error {
                    Text(error).foregroundStyle(.orange)
                } else if let at = voice.recordedAt {
                    Text(
                        "Recorded \(at.formatted(date: .abbreviated, time: .shortened)). Each call sends 3 seconds of it to your Mac, so only your voice stops Claude, from your first word."
                    )
                } else {
                    Text(
                        "Until you record it, Claude learns your voice from the first few seconds you speak on each call. Until then, anyone talking nearby can stop it."
                    )
                }
            }
        }
        .navigationTitle("Your Voice")
        .navigationBarTitleDisplayMode(.inline)
        .onDisappear(perform: voice.cancel)
    }
}
