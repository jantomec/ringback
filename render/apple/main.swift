// ringback-render-apple: what a session shows you of a 3D model, rendered with Apple's own
// frameworks (SceneKit, Model I/O) on a Mac without OpenUSD's renderer (docs/design.md, "Showing
// you things"). The fallback: it breaks on some USD files, and can't open glTF, where
// ringback-render (render/README.md) works. Same commands as ringback-render:
//
//   ringback-render-apple still MODEL OUT.png [WIDTH HEIGHT [FRAME]]
//       a picture of the model, framed whole, at its first frame (or FRAME, from 0); prints
//       its facts as JSON:
//       {"animated", "frames", "fps", "start", "end", "up"}
//   ringback-render-apple info
//       which renderer this is and what it opens: {"renderer", "formats"}
import AppKit
import Foundation
import ModelIO
import SceneKit
import SceneKit.ModelIO

/// What it opens: USD through SceneKit, the rest through Model I/O.
let usdFormats = ["usd", "usda", "usdc", "usdz"]
let modelIOFormats = ["stl", "obj", "ply"]

func fail(_ why: String, code: Int32 = 1) -> Never {
    FileHandle.standardError.write(Data((why + "\n").utf8))
    exit(code)
}

/// A model: its scene, how big it is and which way is up, and its animation, in seconds.
struct Model {
    let scene: SCNScene
    let low: SCNVector3, high: SCNVector3
    let zUp: Bool
    let start: Double, end: Double, fps: Double

    var animated: Bool { end > start }
    var frames: Int { animated ? max(1, Int(((end - start) * fps + 1e-6).rounded(.down)) + 1) : 1 }

    init(_ path: String) {
        let url = URL(fileURLWithPath: path)
        let ext = url.pathExtension.lowercased()
        guard FileManager.default.isReadableFile(atPath: path) else { fail("can't read \(path)") }
        // Model I/O has a USD's own timing and up axis, which SceneKit's scene lacks (its frame
        // rate is its own, 60); SceneKit draws it.
        let asset = MDLAsset(url: url)
        if usdFormats.contains(ext) {
            do {
                scene = try SCNScene(url: url, options: [.animationImportPolicy: SCNSceneSource.AnimationImportPolicy.doNotPlay])
            } catch {
                fail("Apple's renderer can't open \(url.lastPathComponent): \(error.localizedDescription)")
            }
        } else if modelIOFormats.contains(ext) {
            guard asset.count > 0 else { fail("Apple's renderer found nothing to draw in \(url.lastPathComponent)") }
            asset.loadTextures()
            scene = SCNScene(mdlAsset: asset)
        } else {
            fail("Apple's renderer can't open .\(ext) files; ringback-render, with OpenUSD and Adobe's plugins, opens glTF and GLB too (render/README.md)")
        }
        let (low, high) = scene.rootNode.boundingBox
        guard high.x >= low.x, high.y >= low.y, high.z >= low.z, high.x - low.x + high.y - low.y + high.z - low.z > 0 else {
            fail("\(url.lastPathComponent) has nothing to draw")
        }
        self.low = low
        self.high = high
        // A USD's up axis is its own; an STL has none, and slicers take it as Z up.
        zUp = ext == "stl" || abs(asset.upAxis.z) > abs(asset.upAxis.y)
        start = asset.startTime
        end = asset.endTime
        fps = asset.frameInterval > 0 ? (1 / asset.frameInterval).rounded() : 24
        // Animations follow the time it's rendered at, so any frame can be drawn.
        scene.rootNode.enumerateHierarchy { node, _ in
            for key in node.animationKeys {
                if let player = node.animationPlayer(forKey: key) {
                    player.animation.usesSceneTimeBase = true
                    player.play()
                }
            }
        }
    }

    var facts: String {
        String(format: #"{"animated": %@, "frames": %d, "fps": %g, "start": %g, "end": %g, "up": "%@"}"#,
               animated ? "true" : "false", frames, fps, start, end, zUp ? "Z" : "Y")
    }
}

/// The view of a model: a camera orbiting a target point, at a distance, looking at it.
struct View {
    var target = SIMD3<Double>(0, 0, 0)
    var azimuth = 0.6, elevation = 0.45, distance = 1.0
    var fov = 40.0  // vertical, in degrees

    func up(_ zUp: Bool) -> SIMD3<Double> { zUp ? [0, 0, 1] : [0, 1, 0] }

    func eye(_ zUp: Bool) -> SIMD3<Double> {
        let flat = distance * cos(elevation), rise = distance * sin(elevation)
        let offset: SIMD3<Double> = zUp ? [flat * sin(azimuth), -flat * cos(azimuth), rise]
                                         : [flat * sin(azimuth), rise, flat * cos(azimuth)]
        return target + offset
    }

    /// The view that frames the whole model, from three quarters in front and above: just far
    /// enough that each corner of its box is in the picture, with a margin.
    static func framing(_ model: Model, aspect: Double) -> View {
        var view = View()
        let low = SIMD3<Double>(Double(model.low.x), Double(model.low.y), Double(model.low.z))
        let high = SIMD3<Double>(Double(model.high.x), Double(model.high.y), Double(model.high.z))
        view.target = (low + high) / 2
        let back = simd_normalize(view.eye(model.zUp) - view.target)
        let right = simd_normalize(simd_cross(view.up(model.zUp), back))
        let up = simd_cross(back, right)
        let tanV = tan(view.fov * .pi / 360), tanH = tanV * aspect
        var distance = 1e-6
        for i in 0..<8 {
            let corner = SIMD3<Double>(i & 1 == 0 ? low.x : high.x, i & 2 == 0 ? low.y : high.y, i & 4 == 0 ? low.z : high.z)
                - view.target
            let nearer = simd_dot(corner, back)
            distance = max(distance, nearer + abs(simd_dot(corner, right)) / tanH, nearer + abs(simd_dot(corner, up)) / tanV)
        }
        view.distance = distance * 1.08
        return view
    }
}

/// Renders a model offscreen on Metal, lit from the camera, as usdview lights one with none of
/// its own.
final class Renderer {
    let renderer: SCNRenderer
    let camera = SCNNode()
    let model: Model

    init(_ model: Model) {
        guard let device = MTLCreateSystemDefaultDevice() else { fail("this Mac has no Metal device") }
        self.model = model
        renderer = SCNRenderer(device: device, options: nil)
        renderer.scene = model.scene
        renderer.autoenablesDefaultLighting = true
        model.scene.background.contents = NSColor(srgbRed: 0.12, green: 0.12, blue: 0.14, alpha: 1)
        camera.camera = SCNCamera()
        model.scene.rootNode.addChildNode(camera)
        renderer.pointOfView = camera
    }

    func render(_ view: View, frame: Int, width: Int, height: Int) -> NSImage {
        let radius = simd_length(SIMD3<Double>(Double(model.high.x - model.low.x), Double(model.high.y - model.low.y),
                                               Double(model.high.z - model.low.z))) / 2
        let lens = camera.camera!
        lens.fieldOfView = view.fov
        lens.projectionDirection = .vertical
        lens.zNear = max(view.distance - radius * 1.5, view.distance * 0.01)
        lens.zFar = view.distance + radius * 2
        let eye = view.eye(model.zUp), target = view.target, up = view.up(model.zUp)
        camera.simdPosition = SIMD3<Float>(Float(eye.x), Float(eye.y), Float(eye.z))
        camera.simdLook(at: SIMD3<Float>(Float(target.x), Float(target.y), Float(target.z)),
                        up: SIMD3<Float>(Float(up.x), Float(up.y), Float(up.z)), localFront: SCNNode.simdLocalFront)
        let time = model.start + Double(min(max(frame, 0), model.frames - 1)) / model.fps
        return renderer.snapshot(atTime: time, with: CGSize(width: width, height: height), antialiasingMode: .multisampling4X)
    }
}

func writePNG(_ image: NSImage, to path: String, width: Int, height: Int) -> Bool {
    guard let cg = image.cgImage(forProposedRect: nil, context: nil, hints: nil) else { return false }
    let bitmap = NSBitmapImageRep(cgImage: cg)
    bitmap.size = NSSize(width: width, height: height)
    guard let data = bitmap.representation(using: .png, properties: [:]) else { return false }
    return (try? data.write(to: URL(fileURLWithPath: path))) != nil
}

let usage = "usage: ringback-render-apple still MODEL OUT.png [WIDTH HEIGHT [FRAME]] | info"
let args = CommandLine.arguments
switch (args.count > 1 ? args[1] : "", args.count) {
case ("still", 4), ("still", 6), ("still", 7):
    let width = args.count >= 6 ? Int(args[4]) ?? 0 : 1200, height = args.count >= 6 ? Int(args[5]) ?? 0 : 900
    let frame = args.count == 7 ? Int(args[6]) ?? -1 : 0
    guard (16...4096).contains(width), (16...4096).contains(height), frame >= 0 else { fail(usage, code: 2) }
    let model = Model(args[2])
    let image = Renderer(model).render(View.framing(model, aspect: Double(width) / Double(height)), frame: frame,
                                       width: width, height: height)
    guard writePNG(image, to: args[3], width: width, height: height) else { fail("can't write \(args[3])") }
    print(model.facts)
case ("info", 2):
    let formats = (usdFormats + modelIOFormats).map { "\"\($0)\"" }.joined(separator: ", ")
    print(#"{"renderer": "apple", "formats": [\#(formats)]}"#)
default:
    fail(usage, code: 2)
}
