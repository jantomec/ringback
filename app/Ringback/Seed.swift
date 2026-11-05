#if DEBUG
import Foundation
import UIKit

/// Sample calls, waiting calls, sessions and what they showed you, for screenshots in a simulator
/// (tools/app-screens), with no hub and no system prompts: launched with
/// `-RingbackSeed calls`, `-RingbackSeed problem` for the connection banner, or
/// `-RingbackSeed setup` for a phone that isn't set up. Only in debug builds; a release build
/// has none of it.
enum Seed {
    static let mode = UserDefaults.standard.string(forKey: "RingbackSeed")
    static var isOn: Bool { mode != nil }
    /// Made-up session IDs, shaped as Claude Code's are.
    private static let ids = [
        "parser": "5c1d7e0a-93b2-4f6e-8a41-2d7c9b0e6f13", "docs-site": "a8e4f2b6-1c3d-4e5f-9a7b-0c2d4e6f8a1b",
        "ringback": "e2b7c4d9-6f1a-4b3c-8d5e-7f9a1b3c5d7e", "ringback-2": "71f3a5c8-2e4b-4d6f-a8c1-3e5f7a9b1c2d",
    ]

    static func fill(_ state: AppState) {
        let now = Date.now
        let today = Calendar.current.startOfDay(for: now)
        let at = { (days: Int, hour: Int, minute: Int) in
            Calendar.current.date(byAdding: DateComponents(day: -days, hour: hour, minute: minute), to: today)!
        }
        let mac = URL(string: "http://10.0.0.2:8788")!
        func call(
            _ id: String, _ project: String, _ subject: String?, _ ringAt: Date, _ outcome: CallRecord.Outcome,
            minutes: Double = 0, back: String? = nil, now: Bool = false, contact: Bool = false, other: Bool = false
        ) -> CallRecord {
            let answered = minutes > 0 ? ringAt : nil
            return CallRecord(
                id: id, project: project, subject: subject, ringAt: ringAt, answeredAt: answered,
                endedAt: answered.map { $0 + minutes * 60 }, outcome: outcome, onAnotherCall: other, voice: mac,
                callsBack: back, calledNow: now, contact: contact, session: Self.ids[project] ?? "0f0e0d0c-\(project)")
        }
        // Times today that have passed, so the rows show times, not days, whenever this runs.
        let hour = Calendar.current.component(.hour, from: now)
        let earlier = { (back: Int) in at(0, max(0, hour - back), 5 * back) }
        state.seed([
            call("c1", "ringback", "Sessions tab built and tried offline", earlier(0), .answered, minutes: 3.2, contact: true),
            call("c2", "ringback", "Answered call backs work offline", earlier(1), .answered, minutes: 4.2),
            call("c3", "parser", "Parser fix ready to push", earlier(2), .missed),
            call("c4", "parser", "Parser fix ready to push", earlier(2), .answered, minutes: 2, back: "c3"),
            call("c5", "docs-site", "Docs build fails on Python 3.14", at(1, 18, 40), .declined),
            call("c6", "infra", "Nightly backup restored", at(2, 9, 15), .failed, back: "c9"),
            call("c7", "ringback", "Weak signal stats in the ended report", at(3, 16, 2), .answered, minutes: 1.4, other: true),
            call("c8", "poems", "Third stanza added, poem finished", at(9, 11, 30), .answered, minutes: 0.8),
        ])
        let tomorrow = Calendar.current.date(byAdding: DateComponents(day: 1, hour: 8), to: today)!
        state.waiting = [
            WaitingCall(
                id: "w1", project: "parser", subject: "Which branch should the fix go to?", voice: mac, heldUntil: tomorrow,
                question: true, session: Self.ids["parser"]!),
            WaitingCall(
                id: "w2", project: "docs-site", subject: "Docs site rebuilt, three links fixed", voice: mac,
                heldUntil: tomorrow, session: Self.ids["docs-site"]!),
        ]
        state.contacts = [
            Contact(
                session: Self.ids["docs-site"]!, project: "docs-site", voice: mac, subject: "Docs site rebuilt, three links fixed",
                briefedAt: at(0, 7, 50), since: at(0, 7, 10)),
            Contact(
                session: Self.ids["parser"]!, project: "parser", voice: mac, subject: "Which branch should the fix go to?",
                briefedAt: earlier(1), since: at(0, 8, 0)),
            Contact(
                session: Self.ids["ringback"]!, project: "ringback", voice: mac, subject: "Sessions tab built and tried offline",
                briefedAt: earlier(0), since: at(0, 9, 12)),
            Contact(
                session: Self.ids["ringback-2"]!, project: "ringback", voice: mac, subject: nil, briefedAt: nil,
                since: earlier(1)),
        ]
        state.shown = shown(earlier: earlier, at: at)
        // On hold until tomorrow's work hours, with the two calls waiting; taking calls for the
        // connection banner's screenshot, until 19:00.
        state.hold = HoldState(on: true, until: tomorrow)
        // Paired, as a phone in use is: the samples have no hub to pair with.
        state.needsPairing = false
        switch mode {
        case "problem":
            state.hold = HoldState(on: false, until: Calendar.current.date(byAdding: .hour, value: 19, to: today))
            state.connection = .failed(reason: "The request timed out.")
            state.lastConnected = at(0, max(0, hour - 2), 10)
        case "speech":
            // No speech computer linked, a call waiting for it, and the microphone off: the
            // speech banner, with one more problem behind it.
            state.hold = HoldState(on: false, until: Calendar.current.date(byAdding: .hour, value: 19, to: today))
            state.connection = .connected(at: now)
            state.status = HubStatus(speechOK: false, waitingForSpeech: 1)
            state.microphoneOff = true
            state.waiting = [
                WaitingCall(
                    id: "w3", project: "parser", subject: "Parser fix ready to push", voice: mac, heldUntil: nil,
                    session: Self.ids["parser"]!, reason: "no speech")
            ]
        case "paired":
            // Just paired from the portal's link: the sheet that says so.
            state.connection = .connected(at: now)
            state.justPaired = true
        case "setup":
            state.connection = .notSetUp
            state.seed([])
            state.waiting = []
            state.shown = []
            state.hold = nil
        default:
            state.connection = .connected(at: now)
        }
    }

    /// What sessions showed you: an image, a PDF, a clip and a model, with their pictures drawn
    /// here and kept where the app keeps what it fetched, so the viewer finds them. The clip has
    /// only its picture, so it doesn't play.
    private static func shown(earlier: (Int) -> Date, at: (Int, Int, Int) -> Date) -> [ShownItem] {
        let folder = ShownView.folder
        try? FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let iso = ISO8601DateFormatter()
        func item(_ id: String, _ project: String, _ kind: String, _ name: String, _ caption: String, _ when: Date,
                  model: [String: Any]? = nil) -> ShownItem? {
            var listed: [String: Any] = [
                "id": id, "session_id": Self.ids[project] ?? "0f0e0d0c-\(project)", "project": project, "kind": kind,
                "name": name, "caption": caption, "at": iso.string(from: when),
            ]
            listed["model"] = model
            return ShownItem(listed)
        }
        let render = picture(hue: 0.6, rings: 5)
        try? render.pngData()?.write(to: folder.appending(path: "s-render-file.png"))
        try? render.jpegData(compressionQuality: 0.7)?.write(to: folder.appending(path: "s-render-thumb.jpg"))
        let plan = UIGraphicsPDFRenderer(bounds: CGRect(x: 0, y: 0, width: 612, height: 792)).pdfData { context in
            context.beginPage()
            ("Release plan\n\n1. Merge the parser fix\n2. Rebuild the docs\n3. Tag 0.4" as NSString).draw(
                at: CGPoint(x: 60, y: 60), withAttributes: [.font: UIFont.systemFont(ofSize: 22)])
        }
        try? plan.write(to: folder.appending(path: "s-plan-file.pdf"))
        try? picture(hue: 0.0, rings: 1).jpegData(compressionQuality: 0.7)?.write(to: folder.appending(path: "s-plan-thumb.jpg"))
        try? picture(hue: 0.3, rings: 3).jpegData(compressionQuality: 0.7)?.write(to: folder.appending(path: "s-clip-thumb.jpg"))
        let bracket = picture(hue: 0.08, rings: 2)
        try? bracket.pngData()?.write(to: folder.appending(path: "s-model-poster.png"))
        try? bracket.jpegData(compressionQuality: 0.7)?.write(to: folder.appending(path: "s-model-thumb.jpg"))
        return [
            item("s-render", "ringback", "image", "render.png", "The render after the lighting fix", earlier(0)),
            item("s-clip", "parser", "video", "run.mov", "The parser on the big file, 12 s", earlier(1)),
            item("s-model", "ringback", "model", "bracket.usdc", "The bracket, version 3", earlier(2),
                 model: ["animated": true, "frames": 48, "fps": 24]),
            item("s-plan", "docs-site", "pdf", "plan.pdf", "Release plan", at(1, 17, 5)),
        ].compactMap { $0 }
    }

    /// A made-up picture: rings on a gradient.
    private static func picture(hue: CGFloat, rings: Int) -> UIImage {
        UIGraphicsImageRenderer(size: CGSize(width: 900, height: 900)).image { context in
            let colors = [UIColor(hue: hue, saturation: 0.5, brightness: 0.9, alpha: 1).cgColor,
                          UIColor(hue: hue + 0.1, saturation: 0.7, brightness: 0.4, alpha: 1).cgColor] as CFArray
            if let gradient = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: colors, locations: [0, 1]) {
                context.cgContext.drawLinearGradient(gradient, start: .zero, end: CGPoint(x: 900, y: 900), options: [])
            }
            UIColor.white.withAlphaComponent(0.8).setStroke()
            for ring in 0..<rings {
                let inset = CGFloat(120 + ring * 60)
                let path = UIBezierPath(ovalIn: CGRect(x: inset, y: inset, width: 900 - 2 * inset, height: 900 - 2 * inset))
                path.lineWidth = 14
                path.stroke()
            }
        }
    }
}
#endif
