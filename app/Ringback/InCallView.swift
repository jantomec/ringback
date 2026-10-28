import AVFoundation
import AVKit
import SwiftUI

/// The call screen, shown while you're on a call. iOS shows its own screen only on a locked
/// phone; when the phone is unlocked, answering brings the app to the front and this is it.
/// It follows the style of iOS's own call screen (iOS 27): timer above the name, round glass
/// buttons with labels, and End in the middle of the bottom row. We only need three buttons,
/// so they share one row.
struct InCallView: View {
    let state: AppState
    let calls: CallService
    @State private var route = AudioRoute.current

    /// Measured from a screenshot of iOS's call screen.
    private static let column: CGFloat = 117

    var body: some View {
        ZStack {
            LinearGradient(
                stops: [
                    .init(color: Color(red: 0.50, green: 0.51, blue: 0.53), location: 0),
                    .init(color: Color(red: 0.30, green: 0.32, blue: 0.30), location: 0.45),
                    .init(color: Color(red: 0.21, green: 0.24, blue: 0.21), location: 0.7),
                    .init(color: Color(red: 0.25, green: 0.27, blue: 0.30), location: 1),
                ], startPoint: .top, endPoint: .bottom
            )
            .ignoresSafeArea()

            if let call = state.activeCall {
                VStack(spacing: 0) {
                    VStack(spacing: 4) {
                        status(call)
                            .font(.system(size: 26, weight: .semibold))
                            .foregroundStyle(.white.opacity(0.5))
                        Text(call.callerName)
                            .font(.system(size: 42, weight: .bold))
                            .multilineTextAlignment(.center)
                            .lineLimit(2)
                            .minimumScaleFactor(0.6)
                        if let subject = call.subject {
                            Text(subject)
                                .font(.title3)
                                .foregroundStyle(.white.opacity(0.6))
                                .multilineTextAlignment(.center)
                                .padding(.top, 6)
                        }
                    }
                    .padding(.top, 56)
                    .padding(.horizontal, 24)

                    Spacer()

                    HStack(spacing: 0) {
                        slot {
                            GlassButton(label: "Audio", on: route.isOn) {
                                Image(systemName: route.symbol)
                            } overlay: {
                                AudioRoutePicker()
                            }
                        }
                        slot { EndButton(action: calls.endActiveCall) }
                        slot {
                            GlassButton(label: "Mute", on: call.muted, action: { calls.setMuted(!call.muted) }) {
                                Image(systemName: "mic.slash.fill")
                            }
                        }
                    }
                    .padding(.bottom, 24)
                }
                .foregroundStyle(.white)
            }
        }
        .preferredColorScheme(.dark)
        .onReceive(NotificationCenter.default.publisher(for: AVAudioSession.routeChangeNotification)) { _ in
            route = .current
        }
    }

    private func slot(@ViewBuilder _ content: () -> some View) -> some View {
        content().frame(width: Self.column)
    }

    /// Like iOS: the timer once there's audio, otherwise what's happening.
    @ViewBuilder
    private func status(_ call: ActiveCall) -> some View {
        switch call.audio {
        case .connected:
            TimelineView(.periodic(from: call.answeredAt, by: 1)) { context in
                Text(Duration.seconds(context.date.timeIntervalSince(call.answeredAt)),
                     format: .time(pattern: .minuteSecond(padMinuteToLength: 2)))
                    .monospacedDigit()
            }
        case .connecting: Text("Connecting…")
        case .none: Text("No audio")
        case .failed(let reason): Text("Audio failed: \(reason)").foregroundStyle(.orange)
        }
    }
}

/// Where the call's sound is going. Like iOS, the Audio button is white unless it's the
/// phone's earpiece.
private enum AudioRoute {
    case earpiece, speaker, airPods, headphones

    static var current: AudioRoute {
        guard let output = AVAudioSession.sharedInstance().currentRoute.outputs.first else { return .earpiece }
        switch output.portType {
        case .builtInSpeaker: return .speaker
        case .bluetoothHFP, .bluetoothA2DP, .bluetoothLE:
            return output.portName.localizedCaseInsensitiveContains("AirPods") ? .airPods : .headphones
        case .headphones, .usbAudio, .carAudio: return .headphones
        default: return .earpiece
        }
    }

    var isOn: Bool { self != .earpiece }

    var symbol: String {
        switch self {
        case .earpiece: "speaker.fill"
        case .speaker: "speaker.wave.3.fill"
        case .airPods: "airpods"
        case .headphones: "headphones"
        }
    }
}

/// A round Liquid Glass button with its label underneath; white with a black icon when on.
private struct GlassButton<Icon: View, Overlay: View>: View {
    let label: String
    let on: Bool
    var action: (() -> Void)? = nil
    @ViewBuilder let icon: Icon
    @ViewBuilder var overlay: Overlay

    var body: some View {
        VStack(spacing: 8) {
            ZStack {
                face
                if let action { Button(action: action) { Color.clear }.buttonStyle(.plain).clipShape(Circle()) }
                overlay
            }
            .frame(width: 78, height: 78)
            Text(label).font(.system(size: 17))
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(label)
        .accessibilityAddTraits(on ? .isSelected : [])
    }

    @ViewBuilder
    private var face: some View {
        let symbol = icon.font(.system(size: 30, weight: .medium)).frame(width: 78, height: 78)
        if on {
            symbol.foregroundStyle(.black).background(.white, in: Circle())
        } else {
            symbol.foregroundStyle(.white).glassEffect(.regular, in: Circle())
        }
    }
}

extension GlassButton where Overlay == EmptyView {
    init(label: String, on: Bool, action: (() -> Void)? = nil, @ViewBuilder icon: () -> Icon) {
        self.label = label
        self.on = on
        self.action = action
        self.icon = icon()
        self.overlay = EmptyView()
    }
}

private struct EndButton: View {
    let action: () -> Void

    var body: some View {
        VStack(spacing: 8) {
            Button(action: action) {
                Image(systemName: "phone.down.fill")
                    .font(.system(size: 32, weight: .medium))
                    .foregroundStyle(.white)
                    .frame(width: 78, height: 78)
                    .background(Color(red: 0.80, green: 0.18, blue: 0.15), in: Circle())
            }
            .buttonStyle(.plain)
            Text("End").font(.system(size: 17))
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("End call")
    }
}

/// iOS's own audio-route picker (AirPods, iPhone, speaker), laid invisibly over the Audio
/// button so a tap opens it while our icon shows the current route.
private struct AudioRoutePicker: UIViewRepresentable {
    func makeUIView(context: Context) -> AVRoutePickerView {
        let picker = AVRoutePickerView()
        picker.tintColor = .clear
        picker.activeTintColor = .clear
        return picker
    }

    func updateUIView(_ view: AVRoutePickerView, context: Context) {}
}
