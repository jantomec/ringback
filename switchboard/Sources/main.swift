// The ringback switchboard: knows the phone, places calls through APNs, hears what happened.
import Foundation

setlinebuf(stdout)  // one log line at a time, also when launchd writes it to a file

let arguments = CommandLine.arguments.dropFirst()
switch arguments.first {
case "serve":
    guard let listen = loadConfig()["listen"] else { fail("No listen address in \(configPath).") }
    serve(listen: listen)
case "ring":
    let options = parseOptions(arguments.dropFirst(), allowed: ["--project", "--subject", "--voice"])
    await ring(project: options["--project"] ?? "ringback", subject: options["--subject"], voice: options["--voice"])
case "devices":
    for (token, d) in Devices.load().sorted(by: { $0.key < $1.key }) {
        print("…\(token.suffix(8))  \(d["device"] ?? "")  \(d["environment"] ?? "")  registered \(d["registered"] ?? "")")
    }
case "-h", "--help":
    print(usage)
default:
    fail(usage)
}
