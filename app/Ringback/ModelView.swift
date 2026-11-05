import SwiftUI
import WebRTC

/// A model's live view (ModelLink): the computer whose session showed it renders it as you
/// turn it, and streams it here. One finger turns it around the model, two move it, a pinch
/// comes closer, and a double tap, or Reset, goes back to where it started. A model that moves
/// has the clip's playback controls. Its picture shows while it connects, and if it can't,
/// with why. Share shares the picture as you see it.
struct ModelView: View {
    let item: ShownItem
    @State private var link = ModelLink()
    @State private var camera: OrbitCamera?
    @State private var poster: UIImage?
    @State private var started = false
    @State private var sharing: URL?
    @Environment(\.displayScale) private var displayScale

    var body: some View {
        GeometryReader { space in
            ZStack {
                Color(red: 0.12, green: 0.12, blue: 0.14)
                if let poster, link.status != .live {
                    Image(uiImage: poster).resizable().scaledToFit()
                }
                if let video = link.video, link.status == .live {
                    VideoSurface(track: video)
                }
                OrbitSurface(
                    orbit: { dx, dy in move { $0.orbit(dx: dx, dy: dy) } },
                    pan: { dx, dy in move { $0.pan(dx: dx, dy: dy, height: space.size.height) } },
                    zoom: { scale in move { $0.zoom(by: scale) } },
                    letGo: letGo, reset: reset)
                status
            }
            .onChange(of: space.size, initial: true) { start(at: space.size) }
            .overlay(alignment: .bottomTrailing) {
                if link.ready != nil {
                    // Labelled, on the model, apart from the toolbar's buttons.
                    Button("Reset", systemImage: "arrow.counterclockwise", action: reset)
                        .font(.body.weight(.medium))
                        .buttonStyle(.glass)
                        .controlSize(.large)
                        .accessibilityLabel("Reset View")
                        .padding(16)
                }
            }
        }
        .task {
            poster = (try? await ShownFiles.fetch(item.id, part: "poster", into: ShownView.folder))
                .flatMap { UIImage(contentsOfFile: $0.path) }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if let ready = link.ready, ready.animated {
                PlaybackBar(isPlaying: link.playing, position: "Frame \(link.frame + 1) of \(ready.frames)") { play($0, ready) }
            }
        }
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Button("Share", systemImage: "square.and.arrow.up", action: share).disabled(link.status != .live)
            }
        }
        .sheet(isPresented: Binding(get: { sharing != nil }, set: { if !$0 { sharing = nil } })) {
            if let sharing { ShareSheet(file: sharing) }
        }
        .onChange(of: link.ready) { camera = camera ?? link.ready?.home }
        .onDisappear { link.stop() }
        .accessibilityElement(children: .contain)
    }

    @ViewBuilder private var status: some View {
        switch link.status {
        case .connecting:
            Label("Connecting to the computer…", systemImage: "cube")
                .font(.footnote).padding(.horizontal, 12).padding(.vertical, 6)
                .background(.thinMaterial, in: .capsule)
                .frame(maxHeight: .infinity, alignment: .top).padding(.top, 8)
        case .live:
            EmptyView()
        case .ended(let why):
            Text(why)
                .font(.footnote).multilineTextAlignment(.center)
                .padding(12).background(.thinMaterial, in: .rect(cornerRadius: 12))
                .frame(maxHeight: .infinity, alignment: .bottom).padding()
        }
    }

    /// Opens the view once the space has its size: as the viewer opens, the space starts tiny,
    /// and the picture would be rendered that small. Rendered at the screen's pixels, at most
    /// 1920 on its long side.
    private func start(at size: CGSize) {
        guard !started, size.width >= 200, size.height >= 200 else { return }
        started = true
        let scale = min(displayScale, 1920 / max(size.width, size.height))
        let width = Int(size.width * scale) / 2 * 2, height = Int(size.height * scale) / 2 * 2
        Task { await link.start(item, width: width, height: height) }
    }

    /// Shares the picture as you see it, as a PNG named for its project.
    private func share() {
        guard let picture = link.picture()?.pngData() else { return }
        let file = FileManager.default.temporaryDirectory.appending(path: "\(item.project).png")
        guard (try? picture.write(to: file)) != nil else { return }
        sharing = file
    }

    /// Moves the camera as your fingers do, and sends it at once: as often as the screen
    /// redraws, the touches coming that often.
    private func move(_ change: (inout OrbitCamera) -> Void) {
        guard var moved = camera, link.status == .live else { return }
        change(&moved)
        camera = moved
        link.moving(moved.input)
    }

    /// Where you let go, sent so it surely arrives.
    private func letGo() {
        if let camera { link.send(camera.input) }
    }

    private func reset() {
        guard let home = link.ready?.home else { return }
        camera = home
        link.send(home.input)
    }

    /// The playback controls, as a clip's: stepping pauses it, and the end is its last frame.
    private func play(_ action: PlaybackBar.Action, _ ready: ModelLink.Ready) {
        let last = ready.frames - 1
        switch action {
        case .playPause:
            link.send(["play": !link.playing])
            link.expect(frame: link.frame, playing: !link.playing)
        case .start, .back, .forward, .end:
            let to = switch action {
            case .start: 0
            case .back: max(link.frame - 1, 0)
            case .forward: min(link.frame + 1, last)
            default: last
            }
            link.send(["frame": to])
            link.expect(frame: to, playing: false)
        }
    }
}

/// The share sheet, for a file.
private struct ShareSheet: UIViewControllerRepresentable {
    let file: URL

    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: [file], applicationActivities: nil)
    }

    func updateUIViewController(_ controller: UIActivityViewController, context: Context) {}
}

/// The model's video, fitted to the space it has.
private struct VideoSurface: UIViewRepresentable {
    let track: RTCVideoTrack

    func makeUIView(context: Context) -> RTCMTLVideoView {
        let view = RTCMTLVideoView(frame: .zero)
        view.videoContentMode = .scaleAspectFit
        track.add(view)
        return view
    }

    func updateUIView(_ view: RTCMTLVideoView, context: Context) {}

    static func dismantleUIView(_ view: RTCMTLVideoView, coordinator: ()) {
        view.removeFromSuperview()
    }
}

/// Where the gestures land, over the video: one finger drags to turn it, two drag to move it
/// and pinch to come closer, together, and a double tap resets it.
private struct OrbitSurface: UIViewRepresentable {
    let orbit: (Double, Double) -> Void
    let pan: (Double, Double) -> Void
    let zoom: (Double) -> Void
    let letGo: () -> Void
    let reset: () -> Void

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> UIView {
        let view = UIView()
        view.backgroundColor = .clear
        let coordinator = context.coordinator
        let turn = UIPanGestureRecognizer(target: coordinator, action: #selector(Coordinator.turn(_:)))
        turn.maximumNumberOfTouches = 1
        let move = UIPanGestureRecognizer(target: coordinator, action: #selector(Coordinator.move(_:)))
        move.minimumNumberOfTouches = 2
        let pinch = UIPinchGestureRecognizer(target: coordinator, action: #selector(Coordinator.pinch(_:)))
        let twice = UITapGestureRecognizer(target: coordinator, action: #selector(Coordinator.twice(_:)))
        twice.numberOfTapsRequired = 2
        for recognizer in [turn, move, pinch, twice] as [UIGestureRecognizer] {
            recognizer.delegate = coordinator
            view.addGestureRecognizer(recognizer)
        }
        view.isAccessibilityElement = true
        view.accessibilityLabel = String(localized: "3D model")
        view.accessibilityHint = String(localized: "Drag to turn it, with two fingers to move it, and pinch to zoom.")
        return view
    }

    func updateUIView(_ view: UIView, context: Context) {
        context.coordinator.surface = self
    }

    final class Coordinator: NSObject, UIGestureRecognizerDelegate {
        var surface: OrbitSurface?

        @objc func turn(_ recognizer: UIPanGestureRecognizer) {
            let moved = recognizer.translation(in: recognizer.view)
            recognizer.setTranslation(.zero, in: recognizer.view)
            surface?.orbit(moved.x, moved.y)
            ended(recognizer)
        }

        @objc func move(_ recognizer: UIPanGestureRecognizer) {
            let moved = recognizer.translation(in: recognizer.view)
            recognizer.setTranslation(.zero, in: recognizer.view)
            surface?.pan(moved.x, moved.y)
            ended(recognizer)
        }

        @objc func pinch(_ recognizer: UIPinchGestureRecognizer) {
            surface?.zoom(recognizer.scale)
            recognizer.scale = 1
            ended(recognizer)
        }

        private func ended(_ recognizer: UIGestureRecognizer) {
            if recognizer.state == .ended || recognizer.state == .cancelled { surface?.letGo() }
        }

        @objc func twice(_ recognizer: UITapGestureRecognizer) {
            surface?.reset()
        }

        /// Two fingers move and pinch at once.
        func gestureRecognizer(_ first: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith second: UIGestureRecognizer) -> Bool {
            first is UIPinchGestureRecognizer || second is UIPinchGestureRecognizer
        }
    }
}
