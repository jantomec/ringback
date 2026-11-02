import AppIntents
import Foundation
import Security
import WidgetKit

/// The phone's hold switch (Stage 5 step 2 in docs/design.md): on hold, every call waits; taking
/// calls, they ring. The switchboard keeps it, and the work hours set it at their edges: on hold
/// when they end, taking calls when they start. In between, you set it in the app (the pill
/// beside the tabs, HoldButton) or in Control Center (HoldControl). This file is in both the app
/// and its Control Center extension.
struct HoldState: Codable, Equatable {
    var on: Bool
    /// When the work hours next change it by themselves; nil if they won't.
    var until: Date?

    /// As it is at `date`: past `until`, the hours have changed it.
    func at(_ date: Date) -> HoldState {
        guard let until, date >= until else { return self }
        return HoldState(on: !on, until: nil)
    }
}

/// What the app shares with its Control Center extension: the switchboard's address and the
/// push token that names this phone to it, which the app writes once the switchboard has taken
/// its registration, and the switch as last heard. In a keychain group the two share, rather
/// than an App Group: the team's provisioning profiles allow any keychain group of the team,
/// while an App Group needs a capability added to each App ID. Readable once the phone has been
/// unlocked after a restart, so the control works on a locked phone.
enum Shared {
    private static let group = Bundle.main.object(forInfoDictionaryKey: "RingbackKeychainGroup") as? String

    static var switchboard: URL? {
        get { read("switchboard").flatMap { URL(dataRepresentation: $0, relativeTo: nil) } }
        set { write("switchboard", newValue?.dataRepresentation) }
    }

    static var token: String? {
        get { read("token").map { String(decoding: $0, as: UTF8.self) } }
        set { write("token", newValue.map { Data($0.utf8) }) }
    }

    static var hold: HoldState? {
        get { read("hold").flatMap { try? JSONDecoder().decode(HoldState.self, from: $0) } }
        set { write("hold", newValue.flatMap { try? JSONEncoder().encode($0) }) }
    }

    /// How many calls wait, as last heard, for the Lock Screen widget.
    static var waiting: Int? {
        get { read("waiting").map { Int(String(decoding: $0, as: UTF8.self)) ?? 0 } }
        set { write("waiting", newValue.map { Data(String($0).utf8) }) }
    }

    private static func item(_ key: String) -> [String: Any]? {
        guard let group else { return nil }
        return [
            kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: "ringback",
            kSecAttrAccount as String: key, kSecAttrAccessGroup as String: group,
        ]
    }

    private static func read(_ key: String) -> Data? {
        guard var query = item(key) else { return nil }
        query[kSecReturnData as String] = true
        var found: CFTypeRef?
        return SecItemCopyMatching(query as CFDictionary, &found) == errSecSuccess ? found as? Data : nil
    }

    private static func write(_ key: String, _ data: Data?) {
        guard let query = item(key) else { return }
        SecItemDelete(query as CFDictionary)
        guard let data else { return }
        var added = query
        added[kSecValueData as String] = data
        added[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        SecItemAdd(added as CFDictionary, nil)
    }
}

/// Reads and sets the switch at the switchboard (`/hold`), and on taking calls tells each Mac
/// that holds calls to ask the switchboard again now (`/reask` on its voice server, with the
/// waiting calls' IDs), so they ring at once rather than within the 10 min a held call waits
/// at most before asking again.
enum HoldSwitch {
    /// The Control Center control's kind, which the app reloads when the switch changes.
    static let controlKind = "hold"
    /// The Lock Screen widget's kind (HoldWidget), reloaded when the switch or the waiting
    /// calls change.
    static let widgetKind = "hold-status"

    /// The switch at the switchboard; throws if it can't be reached, or the phone hasn't
    /// registered there yet.
    static func fetch() async throws -> HoldState {
        try await remember(state(post("hold", ["token": try token()], timeout: 4)))
    }

    /// Sets it. Taking calls, the waiting calls' Macs ask again now; one that can't be reached
    /// asks again by itself within 10 min.
    @discardableResult
    static func set(_ on: Bool) async throws -> HoldState {
        let token = try token()
        let set = try await remember(state(post("hold", ["token": token, "hold": on])))
        if !on { await reask(token: token) }
        return set
    }

    /// The switch as last heard, as it is now, without asking the switchboard.
    static var cached: HoldState? {
        Shared.hold?.at(.now)
    }

    /// The switch and how many calls wait, from the switchboard (`/waiting`), or as last heard
    /// if it can't be reached. For the Lock Screen widget, which mustn't reload itself.
    static func status() async -> (hold: HoldState?, waiting: Int) {
        if let token = Shared.token, let data = try? await post("waiting", ["token": token], timeout: 5),
            let answer = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
            let calls = answer["calls"] as? [[String: Any]]
        {
            remember(waiting: calls.count, reload: false)
            if let hold = (answer["hold"] as? [String: Any]).flatMap(state(from:)) { remember(hold, reload: false) }
        }
        return (cached, Shared.waiting ?? 0)
    }

    /// How many calls wait, as the app last fetched them: kept for the Lock Screen widget.
    static func remember(waiting: Int, reload: Bool = true) {
        guard waiting != Shared.waiting else { return }
        Shared.waiting = waiting
        if reload { WidgetCenter.shared.reloadTimelines(ofKind: widgetKind) }
    }

    private static func reask(token: String) async {
        guard let data = try? await post("waiting", ["token": token], timeout: 5),
            let calls = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["calls"] as? [[String: Any]]
        else { return }
        var byMac: [String: [String]] = [:]
        for call in calls {
            if let voice = call["voice"] as? String, let id = call["call_id"] as? String { byMac[voice, default: []].append(id) }
        }
        await withTaskGroup(of: Void.self) { group in
            for (voice, ids) in byMac {
                guard let url = URL(string: voice)?.appending(path: "reask") else { continue }
                group.addTask { _ = try? await send(url, ["call_ids": ids], timeout: 5) }
            }
        }
    }

    private static func token() throws -> String {
        guard let token = Shared.token else { throw URLError(.userAuthenticationRequired) }
        return token
    }

    /// The switch as the switchboard last gave it, also with the waiting calls: kept for the
    /// Control Center control, which reloads when it changes.
    @discardableResult
    static func remember(_ state: HoldState, reload: Bool = true) -> HoldState {
        if state != Shared.hold {
            Shared.hold = state
            ControlCenter.shared.reloadControls(ofKind: controlKind)
            if reload { WidgetCenter.shared.reloadTimelines(ofKind: widgetKind) }
        }
        return state
    }

    /// The switchboard's answer: `{"hold": Bool, "until": ISO 8601}`.
    private static func state(_ data: Data) throws -> HoldState {
        guard let answer = try JSONSerialization.jsonObject(with: data) as? [String: Any], let state = state(from: answer)
        else { throw URLError(.cannotParseResponse) }
        return state
    }

    private static func state(from answer: [String: Any]) -> HoldState? {
        guard let on = answer["hold"] as? Bool else { return nil }
        return HoldState(on: on, until: (answer["until"] as? String).flatMap { ISO8601DateFormatter().date(from: $0) })
    }

    private static func post(_ path: String, _ body: [String: Any], timeout: TimeInterval = 10) async throws -> Data {
        guard let base = Shared.switchboard else { throw URLError(.badURL) }
        return try await send(base.appending(path: path), body, timeout: timeout)
    }

    private static func send(_ url: URL, _ body: [String: Any], timeout: TimeInterval) async throws -> Data {
        var request = URLRequest(url: url, timeoutInterval: timeout)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (data, response) = try await URLSession.shared.data(for: request)
        guard (response as? HTTPURLResponse)?.statusCode == 200 else { throw URLError(.badServerResponse) }
        return data
    }
}

/// Puts calls on hold, or takes them: what the Control Center control runs, in the extension,
/// without opening the app.
struct SetHoldIntent: SetValueIntent {
    static let title: LocalizedStringResource = "Hold Calls"
    static let description = IntentDescription("Holds every call from your Claude Code sessions until you take calls.")

    @Parameter(title: "On hold")
    var value: Bool

    func perform() async throws -> some IntentResult {
        try await HoldSwitch.set(value)
        return .result()
    }
}
