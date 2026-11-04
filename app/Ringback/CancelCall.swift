import Foundation
import UIKit

/// Cancels a waiting call, so it never rings. The hub takes it off the waiting calls at once, so
/// the calls after it can ring, and tells the calling Mac's agent, now or when it's back, which
/// forgets it and tells the session; what the call asked goes back to the session's terminal.
/// It goes to the hub, as a call's audio does, with the call's ID.
enum CancelCall {
    /// Asked first, for a call whose session waits for your answer to its question.
    static let confirmTitle = "The session still waits for your answer"
    static let confirmMessage = "Its question goes to the terminal and the Claude app."

    /// Sends it. Taken, the call leaves the list; one that no longer waits (it rang meanwhile,
    /// or was cancelled) just leaves it; otherwise an alert says why. Either way the list is
    /// fetched again, for the badge and the widget. It first reaches the hub, as a call does,
    /// for a Nebula tunnel that's gone stale. True if it was cancelled.
    @MainActor
    @discardableResult
    static func send(_ call: WaitingCall, state: AppState) async -> Bool {
        let task = UIApplication.shared.beginBackgroundTask(withName: "cancel a call")
        defer { UIApplication.shared.endBackgroundTask(task) }
        guard await AudioLink.reach(call.voice, until: .now + 20) != nil else {
            state.callBackProblem = ConnectionCheck.message
            return false
        }
        var cancelled = false
        do {
            var request = URLRequest(url: call.voice.appending(path: "cancel"), timeoutInterval: 10)
            request.httpMethod = "POST"
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONSerialization.data(withJSONObject: ["call_id": call.id])
            let (_, response) = try await URLSession.shared.data(for: request)
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            if status == 200 {
                state.waiting.removeAll { $0.id == call.id }
                cancelled = true
            } else if status != 403 {
                state.callBackProblem = "Your hub didn't cancel the call (HTTP \(status))."
            }
        } catch {
            state.callBackProblem = ConnectionCheck.message
        }
        await state.refreshWaiting()
        return cancelled
    }
}
