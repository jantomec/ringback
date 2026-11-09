import Foundation

/// Something a session showed you ("Showing you things" in docs/design.md): an image, a PDF, a
/// clip, a 3D model, or text it sent you (Markdown), which the hub keeps for a week. Text has no
/// picture: it's listed by its start, as plain text (preview). A PDF, a clip, a model and an image a
/// notification can't show have a poster, a picture of it; a model has only its poster, since
/// the model stays on its Mac. This file is in both the app and its notification service
/// extension, which fetches what a notification shows.
struct ShownItem: Identifiable, Equatable {
    enum Kind: String {
        case image, pdf, video, model, text
    }

    let id: String
    let session: String
    let project: String
    let kind: Kind
    /// The file's name on the Mac.
    let name: String
    let caption: String
    let at: Date
    /// The file's content type, if the hub has the file.
    let fileType: String?
    /// The poster's content type, if it has one.
    let posterType: String?
    /// A model's animation: how many frames, at how many a second; nil for one that has none,
    /// and for the other kinds.
    let animation: (frames: Int, fps: Double)?
    /// Text's start as plain text, "" for the other kinds.
    let preview: String
    /// Where text that's only a web link goes; nil for the rest.
    let url: URL?

    /// What it shows, in a word or a line: its caption, or else text's first line, or the file's
    /// name.
    var title: String {
        if !caption.isEmpty { return caption }
        if kind == .text, let line = preview.split(separator: "\n").first { return String(line) }
        return name
    }

    /// One as the hub lists it (`/shown`); nil if it's missing what every one has.
    init?(_ listed: [String: Any]) {
        guard let id = listed["id"] as? String, let session = listed["session_id"] as? String,
            let project = listed["project"] as? String, let kind = (listed["kind"] as? String).flatMap(Kind.init),
            let at = (listed["at"] as? String).flatMap({ ISO8601DateFormatter().date(from: $0) })
        else { return nil }
        self.id = id
        self.session = session
        self.project = project
        self.kind = kind
        self.at = at
        name = listed["name"] as? String ?? kind.rawValue
        caption = listed["caption"] as? String ?? ""
        fileType = (listed["file"] as? [String: Any])?["type"] as? String
        posterType = (listed["poster"] as? [String: Any])?["type"] as? String
        preview = listed["preview"] as? String ?? ""
        url = (listed["url"] as? String).flatMap(ShownItem.webURL)
        let model = listed["model"] as? [String: Any]
        if model?["animated"] as? Bool == true, let frames = model?["frames"] as? Int {
            animation = (frames, model?["fps"] as? Double ?? 24)
        } else {
            animation = nil
        }
    }

    /// A web address as a URL; nil for any other.
    static func webURL(_ address: String) -> URL? {
        guard let url = URL(string: address), ["http", "https"].contains(url.scheme?.lowercased() ?? "") else { return nil }
        return url
    }

    static func == (a: ShownItem, b: ShownItem) -> Bool {
        a.id == b.id && a.caption == b.caption && a.fileType == b.fileType && a.posterType == b.posterType
    }
}

/// Fetches what sessions show you from the hub, by the phone's push token and key, as the app's
/// other requests: the list, and each one's file, poster or thumbnail ("thumb", a small JPEG
/// every one has, for lists).
enum ShownFiles {
    /// What the hub keeps, newest first.
    static func list() async throws -> [ShownItem] {
        guard let base = Shared.switchboard, let token = Shared.token else { throw URLError(.userAuthenticationRequired) }
        var request = URLRequest(url: base.appending(path: "shown"), timeoutInterval: 10)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["token": token, "key": Shared.key ?? ""])
        let (data, response) = try await URLSession.shared.data(for: request)
        guard (response as? HTTPURLResponse)?.statusCode == 200,
            let listed = (try JSONSerialization.jsonObject(with: data) as? [String: Any])?["shown"] as? [[String: Any]]
        else { throw URLError(.badServerResponse) }
        return listed.compactMap(ShownItem.init)
    }

    /// Fetches one's file or poster (part) into folder, named for its type so the system knows
    /// it ("<id>-poster.png"); the copy there already if there is one.
    static func fetch(_ id: String, part: String, into folder: URL, timeout: TimeInterval = 120) async throws -> URL {
        let prefix = "\(id)-\(part)."
        if let kept = try? FileManager.default.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil)
            .first(where: { $0.lastPathComponent.hasPrefix(prefix) })
        {
            return kept
        }
        guard let base = Shared.switchboard, let token = Shared.token else { throw URLError(.userAuthenticationRequired) }
        var request = URLRequest(url: base.appending(path: "shown/file"), timeoutInterval: timeout)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: [
            "token": token, "key": Shared.key ?? "", "id": id, "part": part,
        ])
        let (downloaded, response) = try await URLSession.shared.download(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            try? FileManager.default.removeItem(at: downloaded)
            throw URLError(.badServerResponse)
        }
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let file = folder.appending(path: prefix + fileExtension(for: http.value(forHTTPHeaderField: "Content-Type")))
        try? FileManager.default.removeItem(at: file)
        try FileManager.default.moveItem(at: downloaded, to: file)
        return file
    }

    /// The extension a file of this content type has.
    static func fileExtension(for type: String?) -> String {
        switch type?.split(separator: ";").first.map({ $0.trimmingCharacters(in: .whitespaces) }) {
        case "image/png": "png"
        case "image/jpeg": "jpg"
        case "image/gif": "gif"
        case "image/heic": "heic"
        case "image/webp": "webp"
        case "application/pdf": "pdf"
        case "video/mp4": "mp4"
        case "video/quicktime": "mov"
        case "video/x-m4v": "m4v"
        case "text/markdown": "md"
        default: "bin"
        }
    }
}
