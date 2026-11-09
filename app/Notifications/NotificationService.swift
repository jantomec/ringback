import Intents
import UIKit
import UserNotifications

/// What a session shows you ("Showing you things" in docs/design.md) arrives as the hub's
/// notification, naming it (`shown`) and the part to show with it (`part`: its poster, or an
/// image itself, or nothing for text). This fetches that part and attaches it, then makes the notification a
/// message from the session: under its project's name, with its monogram, as its calls ring.
/// Each step that fails leaves the notification as it came, so it always shows. It has about
/// 30 s, and little memory: the part is downloaded to a file, never held.
final class NotificationService: UNNotificationServiceExtension {
    private var deliver: ((UNNotificationContent) -> Void)?
    private var content: UNMutableNotificationContent?
    private let lock = NSLock()

    override func didReceive(
        _ request: UNNotificationRequest, withContentHandler handler: @escaping (UNNotificationContent) -> Void
    ) {
        guard let content = request.content.mutableCopy() as? UNMutableNotificationContent,
            let id = content.userInfo["shown"] as? String
        else { return handler(request.content) }
        deliver = handler
        self.content = content
        let part = content.userInfo["part"] as? String ?? "poster"
        let session = content.userInfo["session_id"] as? String ?? id
        Task {
            let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
            // Text has nothing to attach ("part" is empty): it shows its start.
            if !part.isEmpty, let file = try? await ShownFiles.fetch(id, part: part, into: folder, timeout: 20),
                let attachment = try? UNNotificationAttachment(identifier: id, url: file)
            {
                content.attachments = [attachment]
            }
            finish(await Self.fromSession(content, session: session))
        }
    }

    override func serviceExtensionTimeWillExpire() {
        if let content { finish(content) }
    }

    /// Delivers it once: when it's ready, or when time runs out, whichever comes first.
    private func finish(_ content: UNNotificationContent) {
        lock.lock()
        let deliver = deliver
        self.deliver = nil
        lock.unlock()
        deliver?(content)
    }

    /// The notification as a message from the session (a communication notification): its
    /// project's name, which the hub put in the title, and its monogram, as the app shows it.
    /// As it came, if iOS won't take it.
    private static func fromSession(_ content: UNMutableNotificationContent, session: String) async -> UNNotificationContent {
        let project = content.title
        let sender = INPerson(
            personHandle: INPersonHandle(value: session, type: .unknown), nameComponents: nil, displayName: project,
            image: monogram(project).flatMap { INImage(imageData: $0) }, contactIdentifier: nil, customIdentifier: session)
        let intent = INSendMessageIntent(
            recipients: nil, outgoingMessageType: .outgoingMessageText, content: content.body, speakableGroupName: nil,
            conversationIdentifier: session, serviceName: nil, sender: sender, attachments: nil)
        let interaction = INInteraction(intent: intent, response: nil)
        interaction.direction = .incoming
        do {
            try await interaction.donate()
            return try content.updating(from: intent)
        } catch {
            return content
        }
    }

    /// A project's monogram, as the app draws it: up to two letters on a grey circle.
    private static func monogram(_ project: String) -> Data? {
        let words = project.split { !$0.isLetter && !$0.isNumber }
        let letters = String(words.prefix(2).compactMap(\.first)).uppercased() as NSString
        let size = CGSize(width: 120, height: 120)
        return UIGraphicsImageRenderer(size: size).pngData { context in
            let colors = [UIColor(white: 0.66, alpha: 1).cgColor, UIColor(white: 0.47, alpha: 1).cgColor] as CFArray
            if let gradient = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: colors, locations: [0, 1]) {
                context.cgContext.addEllipse(in: CGRect(origin: .zero, size: size))
                context.cgContext.clip()
                context.cgContext.drawLinearGradient(gradient, start: .zero, end: CGPoint(x: 0, y: size.height), options: [])
            }
            let font = UIFont.systemFont(ofSize: 50, weight: .semibold).withDesign(.rounded)
            let attributes: [NSAttributedString.Key: Any] = [.font: font, .foregroundColor: UIColor.white]
            let drawn = letters.size(withAttributes: attributes)
            letters.draw(
                at: CGPoint(x: (size.width - drawn.width) / 2, y: (size.height - drawn.height) / 2), withAttributes: attributes)
        }
    }
}

private extension UIFont {
    func withDesign(_ design: UIFontDescriptor.SystemDesign) -> UIFont {
        fontDescriptor.withDesign(design).map { UIFont(descriptor: $0, size: pointSize) } ?? self
    }
}
