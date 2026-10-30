import Foundation
import UIKit

/// Sends a waiting call's question to the Claude app instead of the call. The calling Mac's
/// voice server releases the question to the session's own dialog, which Remote Control shows
/// in the Claude app, and forgets the call, so it never rings. It goes over Nebula, as a
/// call's audio does, with the call's ID.
enum ClaudeApp {
    /// While its session last said Remote Control is off, the action only says why it can't.
    static let remoteControlOff = """
        Ringback last heard that Remote Control is off in this session, so the Claude app \
        can't show its question. The call rings when your work hours start.
        """
    private static let unreachable = "Can't reach the Mac that's calling over Nebula. Check that Nebula is on."
    /// Why the voice server didn't take it, as it says.
    private static let refusals = [
        "not waiting": "This call isn't waiting any more.",
        "not listening": "The session isn't listening any more: ringback is off in it, or it has ended.",
        "no question": "This call has no question any more.",
    ]

    /// Sends it. Taken, the call leaves the list, here and at the switchboard ("claude app").
    /// Otherwise an alert says why, and the list is fetched again. It first reaches the voice
    /// server, as a call does, for a Nebula tunnel that's gone stale.
    @MainActor
    static func send(_ call: WaitingCall, state: AppState) async {
        let task = UIApplication.shared.beginBackgroundTask(withName: "to the Claude app")
        defer { UIApplication.shared.endBackgroundTask(task) }
        guard await AudioLink.reach(call.voice, until: .now + 20) != nil else {
            state.callBackProblem = unreachable
            return
        }
        do {
            var request = URLRequest(url: call.voice.appending(path: "claude-app"), timeoutInterval: 10)
            request.httpMethod = "POST"
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONSerialization.data(withJSONObject: ["call_id": call.id])
            let (data, response) = try await URLSession.shared.data(for: request)
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            if status == 200 {
                state.waiting.removeAll { $0.id == call.id }
                Switchboard.report(callID: call.id, event: "claude app")
                return
            }
            let error = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["error"] as? String
            state.callBackProblem = refusals[error ?? ""] ?? "The Mac that's calling didn't take it (HTTP \(status))."
        } catch {
            state.callBackProblem = unreachable
        }
        await state.refreshWaiting()
    }
}
