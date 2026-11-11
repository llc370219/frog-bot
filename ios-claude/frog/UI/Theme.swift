import SwiftUI
import UIKit

extension UIColor {
    convenience init(hex: UInt32, alpha: CGFloat = 1) {
        self.init(red: CGFloat((hex >> 16) & 0xff) / 255,
                  green: CGFloat((hex >> 8) & 0xff) / 255,
                  blue: CGFloat(hex & 0xff) / 255,
                  alpha: alpha)
    }
}

extension Color {
    /// 随系统浅色 / 深色模式自动切换的颜色
    static func dynamic(_ light: UInt32, _ dark: UInt32, alpha: CGFloat = 1, darkAlpha: CGFloat? = nil) -> Color {
        Color(uiColor: UIColor { t in
            t.userInterfaceStyle == .dark ? UIColor(hex: dark, alpha: darkAlpha ?? alpha) : UIColor(hex: light, alpha: alpha)
        })
    }
}

/// 和 Mac 版同一套配色：中性灰白、细分隔线；强调色是青蛙绿，等你确认的事用橙色。
enum Theme {
    static let canvas = Color(uiColor: .systemBackground)
    static let grouped = Color(uiColor: .systemGroupedBackground)
    static let s1 = Color(uiColor: .secondarySystemGroupedBackground)
    static let s2 = Color.dynamic(0xf2f2f4, 0x1c1c1e)          // 卡片底
    static let s3 = Color.dynamic(0x000000, 0xffffff, alpha: 0.08, darkAlpha: 0.12)
    static let bubble = Color.dynamic(0xe9e9eb, 0x2c2c2e)
    static let code = Color.dynamic(0xececee, 0x232325)
    static let field = Color.dynamic(0xffffff, 0x1c1c1e)

    static let fg = Color.dynamic(0x1d1d1f, 0xf5f5f7)
    static let fg2 = Color.dynamic(0x3a3a3c, 0xd1d1d6)
    static let fg3 = Color.dynamic(0x6e6e73, 0xa1a1a6)
    static let fg4 = Color.dynamic(0x8a8a8e, 0x8d8d93)

    static let line = Color.dynamic(0x000000, 0xffffff, alpha: 0.09, darkAlpha: 0.10)
    static let line2 = Color.dynamic(0x000000, 0xffffff, alpha: 0.16, darkAlpha: 0.18)

    static let accent = Color.dynamic(0x2f7d52, 0x4cc38a)
    static let onAccent = Color.dynamic(0xffffff, 0x0b1f14)
    static let accentSoft = Color.dynamic(0x2f7d52, 0x4cc38a, alpha: 0.11, darkAlpha: 0.18)

    static let ok = Color.dynamic(0x248a3d, 0x4cd964)
    static let warn = Color.dynamic(0xa35400, 0xffb340)
    static let warnDot = Color.dynamic(0xf08c00, 0xff9f0a)
    static let warnSoft = Color.dynamic(0xf08c00, 0xff9f0a, alpha: 0.13, darkAlpha: 0.17)
    static let danger = Color.dynamic(0xc4161c, 0xff6961)
    static let dangerSoft = Color.dynamic(0xc4161c, 0xff6961, alpha: 0.09, darkAlpha: 0.14)

    static let mono = Font.system(size: 13, design: .monospaced)
    /// iPad 上聊天内容的最大宽度
    static let contentWidth: CGFloat = 760
}

// MARK: 常用小部件

struct CardBackground: ViewModifier {
    var fill: Color = Theme.s2
    var border: Color? = nil
    var radius: CGFloat = 14

    func body(content: Content) -> some View {
        content
            .background(fill, in: RoundedRectangle(cornerRadius: radius, style: .continuous))
            .overlay {
                if let border {
                    RoundedRectangle(cornerRadius: radius, style: .continuous).strokeBorder(border, lineWidth: 1)
                }
            }
    }
}

extension View {
    func card(fill: Color = Theme.s2, border: Color? = nil, radius: CGFloat = 14) -> some View {
        modifier(CardBackground(fill: fill, border: border, radius: radius))
    }
}

/// 胶囊按钮：主要（绿色实心）、普通（浅灰底）、危险（红字）
struct PillButtonStyle: ButtonStyle {
    enum Kind { case primary, secondary, danger }
    var kind: Kind = .secondary
    var compact = false

    func makeBody(configuration: Configuration) -> some View {
        PillBody(configuration: configuration, kind: kind, compact: compact)
    }

    private struct PillBody: View {
        let configuration: ButtonStyleConfiguration
        let kind: Kind
        let compact: Bool
        @Environment(\.isEnabled) private var enabled

        var body: some View {
            configuration.label
                .font(.system(size: compact ? 14 : 15, weight: kind == .primary ? .semibold : .medium))
                .lineLimit(1)
                .padding(.horizontal, compact ? 12 : 16)
                .frame(minHeight: compact ? 32 : 40)
                .foregroundStyle(kind == .primary ? Theme.onAccent : kind == .danger ? Theme.danger : Theme.fg)
                .background(kind == .primary ? Theme.accent : Theme.s3, in: Capsule())
                .opacity(enabled ? (configuration.isPressed ? 0.7 : 1) : 0.4)
                .contentShape(Capsule())
        }
    }
}

/// 小标签（风险等级、状态等）
struct Chip: View {
    var text: String
    var systemImage: String? = nil
    var tint: Color = Theme.fg3
    var fill: Color = Theme.s2

    var body: some View {
        HStack(spacing: 3) {
            if let systemImage { Image(systemName: systemImage).font(.system(size: 10, weight: .semibold)) }
            Text(text)
        }
        .font(.system(size: 12, weight: .medium))
        .foregroundStyle(tint)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(fill, in: Capsule())
    }
}

/// 描边小胶囊（「默认」「当前选择」）
struct OutlinePill: View {
    var text: String
    var systemImage: String? = nil
    var tint: Color = Theme.fg3

    var body: some View {
        HStack(spacing: 3) {
            if let systemImage { Image(systemName: systemImage).font(.system(size: 10, weight: .bold)) }
            Text(text)
        }
        .font(.system(size: 12, weight: .medium))
        .foregroundStyle(tint)
        .padding(.horizontal, 8)
        .frame(height: 22)
        .overlay(Capsule().strokeBorder(tint == Theme.fg3 ? Theme.line2 : tint, lineWidth: 1))
    }
}

/// 三个跳动的小点：正在输入
struct TypingDots: View {
    var color: Color = Theme.fg4
    var size: CGFloat = 7

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / 30)) { ctx in
            let t = ctx.date.timeIntervalSinceReferenceDate
            HStack(spacing: size * 0.7) {
                ForEach(0..<3, id: \.self) { i in
                    let phase = (t * 1000 / 1200 + Double(i) * -0.125).truncatingRemainder(dividingBy: 1)
                    let p = phase < 0 ? phase + 1 : phase
                    let lift = p < 0.4 ? sin(p / 0.4 * .pi) : 0
                    Circle()
                        .fill(color)
                        .frame(width: size, height: size)
                        .opacity(0.35 + 0.65 * lift)
                        .offset(y: -2 * lift)
                }
            }
        }
        .frame(height: size + 4)
    }
}

/// 细线转圈（运行中）
struct Spinner: View {
    var size: CGFloat = 16
    var tint: Color = Theme.fg3

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / 30)) { ctx in
            let angle = ctx.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 1) * 360
            ZStack {
                Circle().stroke(Theme.line2, lineWidth: 1.75)
                Circle().trim(from: 0, to: 0.25).stroke(tint, style: StrokeStyle(lineWidth: 1.75, lineCap: .round))
                    .rotationEffect(.degrees(angle))
            }
        }
        .frame(width: size, height: size)
    }
}

/// 出现时轻轻上浮
struct Rise: ViewModifier {
    @State private var shown = false
    func body(content: Content) -> some View {
        content
            .opacity(shown ? 1 : 0)
            .offset(y: shown ? 0 : 5)
            .onAppear { withAnimation(.easeOut(duration: 0.2)) { shown = true } }
    }
}

extension View {
    func rise() -> some View { modifier(Rise()) }
}

/// 自动换行的横向排列（工具标签、选项按钮用）
struct FlowLayout: Layout {
    var spacing: CGFloat = 6

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, rowH: CGFloat = 0, maxX: CGFloat = 0
        for v in subviews {
            let s = v.sizeThatFits(ProposedViewSize(width: width, height: nil))
            if x > 0 && x + s.width > width { x = 0; y += rowH + spacing; rowH = 0 }
            x += s.width + spacing
            rowH = max(rowH, s.height)
            maxX = max(maxX, x - spacing)
        }
        return CGSize(width: min(maxX, width), height: y + rowH)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX, y = bounds.minY, rowH: CGFloat = 0
        for v in subviews {
            let s = v.sizeThatFits(ProposedViewSize(width: bounds.width, height: nil))
            if x > bounds.minX && x + s.width > bounds.maxX { x = bounds.minX; y += rowH + spacing; rowH = 0 }
            v.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(s))
            x += s.width + spacing
            rowH = max(rowH, s.height)
        }
    }
}

@MainActor
func copy(_ text: String) {
    UIPasteboard.general.string = text
    Haptics.tap()
}

@MainActor
enum Haptics {
    static func tap() { UIImpactFeedbackGenerator(style: .light).impactOccurred() }
    static func success() { UINotificationFeedbackGenerator().notificationOccurred(.success) }
    static func error() { UINotificationFeedbackGenerator().notificationOccurred(.error) }
}

/// 一行说明文字（设置页、空状态）
struct Note: View {
    var text: String
    var tone: Color = Theme.fg3
    var body: some View {
        Text(text)
            .font(.footnote)
            .foregroundStyle(tone)
            .fixedSize(horizontal: false, vertical: true)
    }
}
