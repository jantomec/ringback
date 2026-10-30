#if DEBUG
import Foundation

/// Sample calls, waiting calls and contacts, for screenshots in a simulator
/// (tools/app-screens), with no switchboard and no system prompts: launched with
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
            minutes: Double = 0, back: String? = nil, now: Bool? = nil, contact: Bool? = nil, other: Bool? = nil
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
            call("c1", "ringback", "Contacts tab built and tried offline", earlier(0), .answered, minutes: 3.2, contact: true),
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
                session: Self.ids["ringback"]!, project: "ringback", voice: mac, subject: "Contacts tab built and tried offline",
                briefedAt: earlier(0), since: at(0, 9, 12)),
            Contact(
                session: Self.ids["ringback-2"]!, project: "ringback", voice: mac, subject: nil, briefedAt: nil,
                since: earlier(1)),
        ]
        switch mode {
        case "problem":
            state.connection = .failed(reason: "The request timed out.")
            state.lastConnected = at(0, max(0, hour - 2), 10)
        case "setup":
            state.connection = .notSetUp
            state.seed([])
            state.waiting = []
        default:
            state.connection = .connected(at: now)
        }
    }
}
#endif
