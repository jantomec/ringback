import AVFoundation
import PDFKit
import SwiftUI

/// What a session showed you ("Showing you things" in docs/design.md), full screen: an image
/// you can zoom, a PDF, a clip with its playback controls, a model's live view (ModelView), or
/// text it sent you (MarkdownView), or the link it is, if it's only that (LinkCard). It opens from
/// the notification, and from the details of the session's calls. The file comes from the hub,
/// which keeps it for a week, and stays on the phone until iOS clears its caches.
struct ShownView: View {
    let item: ShownItem
    /// For a call during which it's open: its pill, and its screen over this (CallOnTop).
    let state: AppState
    let calls: CallService
    @Environment(\.dismiss) private var dismiss
    @State private var file: URL?
    /// Text's Markdown, once fetched.
    @State private var text: String?
    @State private var failed = false

    /// Where the files it fetched are kept.
    static let folder = URL.cachesDirectory.appending(path: "Shown")

    var body: some View {
        NavigationStack {
            content
                .navigationTitle(item.project)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
                    if let url = item.url {
                        // A link alone goes as its address; the card copies it.
                        ToolbarItem(placement: .topBarLeading) { ShareLink(item: url) }
                    } else if let text {
                        // Text goes as itself, not as a file.
                        ToolbarItemGroup(placement: .topBarLeading) {
                            ShareLink(item: text)
                            Button("Copy", systemImage: "doc.on.doc") { UIPasteboard.general.string = text }
                        }
                    } else if let file {
                        ToolbarItem(placement: .topBarLeading) { ShareLink(item: file) }
                    }
                }
                .safeAreaInset(edge: .top) {
                    Text(item.title).font(.subheadline).foregroundStyle(.secondary)
                        .multilineTextAlignment(.center).padding(.horizontal).padding(.bottom, 4)
                }
        }
        .modifier(CallOnTop(state: state, calls: calls))
        .task { await load() }
    }

    @ViewBuilder
    private var content: some View {
        if item.kind == .model {
            ModelView(item: item)
        } else if item.kind == .text, let url = item.url {
            LinkCard(url: url, title: item.title)
        } else if let text {
            MarkdownView(source: text)
        } else if let file {
            switch item.kind {
            case .image: ZoomableImage(url: file)
            case .pdf: PDFDocumentView(url: file)
            case .video: ClipView(url: file)
            case .model, .text: EmptyView()
            }
        } else if failed {
            ContentUnavailableView(
                "Can't Open It", systemImage: "exclamationmark.triangle",
                description: Text("The hub can't be reached, or no longer keeps it: it keeps what sessions show you for a week."))
        } else {
            ProgressView()
        }
    }

    /// The file, from the hub or kept on the phone; a model has its live view instead.
    private func load() async {
        guard item.kind != .model else { return }
        do {
            let fetched = try await ShownFiles.fetch(item.id, part: "file", into: Self.folder)
            if item.kind == .text {
                text = try String(contentsOf: fetched, encoding: .utf8)
            }
            file = fetched
        } catch {
            failed = true
        }
    }

    /// Clears what was fetched more than a week ago, which the hub no longer keeps either.
    static func clearOld() {
        let files = (try? FileManager.default.contentsOfDirectory(at: folder, includingPropertiesForKeys: [.creationDateKey])) ?? []
        for file in files {
            let created = (try? file.resourceValues(forKeys: [.creationDateKey]))?.creationDate ?? .distantPast
            if Date.now.timeIntervalSince(created) > 8 * 24 * 3600 { try? FileManager.default.removeItem(at: file) }
        }
    }
}

/// What a session showed you, as rows in a call's or a session's details: newest first, each
/// opening it.
struct ShownSection: View {
    let items: [ShownItem]
    let state: AppState

    var body: some View {
        if !items.isEmpty {
            Section("Shown") {
                ForEach(items) { item in
                    Button {
                        state.open(item)
                    } label: {
                        HStack(spacing: 12) {
                            Image(systemName: Self.symbol(item)).font(.title3).frame(width: 28)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(item.title).foregroundStyle(.primary)
                                Text("\(item.at.dayLabel), \(item.at.formatted(date: .omitted, time: .shortened))")
                                    .font(.subheadline).foregroundStyle(.secondary)
                            }
                        }
                    }
                }
            }
        }
    }

    static func symbol(_ kind: ShownItem.Kind) -> String {
        switch kind {
        case .image: "photo"
        case .pdf: "doc.richtext"
        case .video: "film"
        case .model: "cube"
        case .text: "text.alignleft"
        }
    }

    /// An item's symbol: its kind's, or a link's.
    static func symbol(_ item: ShownItem) -> String {
        item.url != nil ? "link" : symbol(item.kind)
    }
}

/// An image you can pinch to zoom and drag around, as in Photos; double-tap zooms in and out.
struct ZoomableImage: UIViewRepresentable {
    let url: URL

    func makeUIView(context: Context) -> ZoomingView { ZoomingView(image: UIImage(contentsOfFile: url.path)) }
    func updateUIView(_ view: ZoomingView, context: Context) {}

    final class ZoomingView: UIScrollView, UIScrollViewDelegate {
        private let imageView: UIImageView

        init(image: UIImage?) {
            imageView = UIImageView(image: image)
            super.init(frame: .zero)
            imageView.contentMode = .scaleAspectFit
            addSubview(imageView)
            delegate = self
            maximumZoomScale = 8
            showsHorizontalScrollIndicator = false
            showsVerticalScrollIndicator = false
            let doubleTap = UITapGestureRecognizer(target: self, action: #selector(zoom(_:)))
            doubleTap.numberOfTapsRequired = 2
            addGestureRecognizer(doubleTap)
            // How far it's zoomed, for VoiceOver and the screenshots' check (tools/app-screens).
            isAccessibilityElement = true
            accessibilityIdentifier = "shown-image"
            accessibilityLabel = String(localized: "Image")
            accessibilityTraits = .image
            accessibilityValue = Self.zoomed(1)
        }

        func scrollViewDidZoom(_ scrollView: UIScrollView) {
            accessibilityValue = Self.zoomed(zoomScale)
        }

        private static func zoomed(_ scale: CGFloat) -> String {
            String(localized: "Zoomed \(Double(scale).formatted(.number.precision(.fractionLength(1))))×")
        }

        required init?(coder: NSCoder) { nil }

        /// Unzoomed, the image fills the view, fitted, and so does what scrolls: zooming then
        /// scales both, so a zoomed image can be dragged around.
        override func layoutSubviews() {
            super.layoutSubviews()
            if zoomScale == minimumZoomScale, imageView.frame.size != bounds.size {
                imageView.frame = CGRect(origin: .zero, size: bounds.size)
                contentSize = bounds.size
            }
        }

        func viewForZooming(in scrollView: UIScrollView) -> UIView? { imageView }

        @objc private func zoom(_ tap: UITapGestureRecognizer) {
            if zoomScale > 1 {
                setZoomScale(1, animated: true)
            } else {
                let point = tap.location(in: imageView)
                zoom(to: CGRect(x: point.x - bounds.width / 6, y: point.y - bounds.height / 6, width: bounds.width / 3, height: bounds.height / 3), animated: true)
            }
        }
    }
}

/// A PDF, its pages one under the other, fitted to the width.
struct PDFDocumentView: UIViewRepresentable {
    let url: URL

    func makeUIView(context: Context) -> PDFView {
        let view = PDFView()
        view.document = PDFDocument(url: url)
        view.autoScales = true
        return view
    }

    func updateUIView(_ view: PDFView, context: Context) {}
}

/// A clip, with its playback controls under it.
struct ClipView: View {
    @State private var clip: ClipPlayer

    init(url: URL) { _clip = State(initialValue: ClipPlayer(url: url)) }

    var body: some View {
        VStack(spacing: 0) {
            PlayerLayerView(player: clip.player)
            PlaybackBar(isPlaying: clip.isPlaying, position: clip.position) { clip.act($0) }
        }
        .onDisappear { clip.player.pause() }
    }
}

/// The playback controls a clip has, and a model's animation will: to the start, back one
/// frame, play or pause, forward one frame, to the end, and which frame it's on.
struct PlaybackBar: View {
    enum Action {
        case start, back, playPause, forward, end
    }

    let isPlaying: Bool
    let position: String
    let act: (Action) -> Void

    var body: some View {
        VStack(spacing: 8) {
            Text(position).font(.footnote.monospacedDigit()).foregroundStyle(.secondary)
            HStack {
                control("backward.end.fill", "To the Start", .start)
                control("backward.frame.fill", "Back One Frame", .back)
                control(isPlaying ? "pause.fill" : "play.fill", isPlaying ? "Pause" : "Play", .playPause).font(.title)
                control("forward.frame.fill", "Forward One Frame", .forward)
                control("forward.end.fill", "To the End", .end)
            }
        }
        .padding(.vertical, 12)
        .frame(maxWidth: .infinity)
        .background(.bar)
    }

    private func control(_ symbol: String, _ label: String, _ action: Action) -> some View {
        Button { act(action) } label: {
            Image(systemName: symbol).font(.title2).frame(maxWidth: .infinity, minHeight: 44)
        }
        .accessibilityLabel(label)
    }
}

/// Plays a clip, and steps it a frame at a time. Stepping pauses it first; the end is its last
/// frame, one frame before its duration.
@Observable
final class ClipPlayer {
    let player: AVPlayer
    private(set) var isPlaying = false
    private(set) var frame = 0
    private(set) var frames = 0
    private var fps: Double = 30
    private var observer: Any?
    private var ended: NSObjectProtocol?

    init(url: URL) {
        player = AVPlayer(url: url)
        player.actionAtItemEnd = .pause
        observer = player.addPeriodicTimeObserver(forInterval: CMTime(value: 1, timescale: 30), queue: .main) { [weak self] time in
            guard let self else { return }
            frame = Int((time.seconds * fps).rounded())
            isPlaying = player.timeControlStatus != .paused
        }
        ended = NotificationCenter.default.addObserver(
            forName: AVPlayerItem.didPlayToEndTimeNotification, object: player.currentItem, queue: .main
        ) { [weak self] _ in self?.isPlaying = false }
        Task { @MainActor [weak self] in await self?.measure(AVURLAsset(url: url)) }
    }

    deinit {
        if let observer { player.removeTimeObserver(observer) }
        if let ended { NotificationCenter.default.removeObserver(ended) }
    }

    /// "Frame 12 of 240".
    var position: String { frames > 0 ? "Frame \(min(frame + 1, frames)) of \(frames)" : " " }

    func act(_ action: PlaybackBar.Action) {
        switch action {
        case .playPause:
            if isPlaying {
                player.pause()
                isPlaying = false
            } else {
                if frames > 0, frame >= frames - 1 { seek(to: 0) }
                player.play()
                isPlaying = true
            }
        case .back, .forward:
            pause()
            player.currentItem?.step(byCount: action == .back ? -1 : 1)
        case .start:
            pause()
            seek(to: 0)
        case .end:
            pause()
            seek(to: max(frames - 1, 0))
        }
    }

    private func pause() {
        player.pause()
        isPlaying = false
    }

    private func seek(to frame: Int) {
        player.seek(to: CMTime(seconds: Double(frame) / fps, preferredTimescale: 600), toleranceBefore: .zero, toleranceAfter: .zero)
        self.frame = frame
    }

    @MainActor
    private func measure(_ asset: AVURLAsset) async {
        guard let track = try? await asset.loadTracks(withMediaType: .video).first,
            let rate = try? await track.load(.nominalFrameRate), rate > 0,
            let duration = try? await asset.load(.duration)
        else { return }
        fps = Double(rate)
        frames = max(Int((duration.seconds * fps).rounded()), 1)
    }
}

/// An AVPlayer's picture, fitted to the space it has.
struct PlayerLayerView: UIViewRepresentable {
    let player: AVPlayer

    func makeUIView(context: Context) -> LayerView {
        let view = LayerView()
        view.playerLayer.player = player
        view.playerLayer.videoGravity = .resizeAspect
        return view
    }

    func updateUIView(_ view: LayerView, context: Context) {}

    final class LayerView: UIView {
        override static var layerClass: AnyClass { AVPlayerLayer.self }
        var playerLayer: AVPlayerLayer { layer as! AVPlayerLayer }
    }
}
