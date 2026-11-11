import AppKit
import SwiftUI

extension NSColor {
    convenience init(hex: UInt32, alpha: CGFloat = 1) {
        self.init(srgbRed: CGFloat((hex >> 16) & 0xff) / 255,
                  green: CGFloat((hex >> 8) & 0xff) / 255,
                  blue: CGFloat(hex & 0xff) / 255,
                  alpha: alpha)
    }
}

extension Color {
    /// 随系统浅色 / 深色模式自动切换的颜色。
    static func dynamic(_ light: UInt32, _ dark: UInt32, alpha: CGFloat = 1, darkAlpha: CGFloat? = nil) -> Color {
        Color(nsColor: NSColor(name: nil) { appearance in
            appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
                ? NSColor(hex: dark, alpha: darkAlpha ?? alpha)
                : NSColor(hex: light, alpha: alpha)
        })
    }
}

/// 扁平的 macOS 风格：中性灰白、细分隔线、无渐变；强调色是青蛙绿，等你确认的事用橙色。
enum Theme {
    // 底色
    static let canvas = Color.dynamic(0xffffff, 0x1e1e1e)
    static let s1 = Color.dynamic(0xffffff, 0x1e1e1e)
    static let s2 = Color.dynamic(0xf5f5f7, 0x262627)      // 分组 / 卡片底
    static let s3 = Color.dynamic(0x000000, 0xffffff, alpha: 0.08, darkAlpha: 0.10) // 选中
    static let hover = Color.dynamic(0x000000, 0xffffff, alpha: 0.045, darkAlpha: 0.06)
    static let composer = Color.dynamic(0xffffff, 0x262627)
    static let bubble = Color.dynamic(0xebebed, 0x333335)
    static let code = Color.dynamic(0xececee, 0x2c2c2e)
    static let button = Color.dynamic(0xffffff, 0x48484a)

    // 文字
    static let fg = Color.dynamic(0x1d1d1f, 0xf5f5f7)
    static let fg2 = Color.dynamic(0x3a3a3c, 0xd1d1d6)
    static let fg3 = Color.dynamic(0x6e6e73, 0xa1a1a6)
    static let fg4 = Color.dynamic(0x737378, 0x98989d)

    // 线
    static let line = Color.dynamic(0x000000, 0xffffff, alpha: 0.09, darkAlpha: 0.09)
    static let line2 = Color.dynamic(0x000000, 0xffffff, alpha: 0.15, darkAlpha: 0.16)

    // 强调
    static let accent = Color.dynamic(0x2f7d52, 0x4cc38a)
    static let accentHover = Color.dynamic(0x276b45, 0x5fd09a)
    static let onAccent = Color.dynamic(0xffffff, 0x0b1f14)
    static let accentSoft = Color.dynamic(0x2f7d52, 0x4cc38a, alpha: 0.10, darkAlpha: 0.16)

    static let ok = Color.dynamic(0x248a3d, 0x4cd964)
    static let warn = Color.dynamic(0xa35400, 0xffb340)
    static let warnDot = Color.dynamic(0xf08c00, 0xff9f0a)
    static let warnSoft = Color.dynamic(0xf08c00, 0xff9f0a, alpha: 0.12, darkAlpha: 0.15)
    static let danger = Color.dynamic(0xc4161c, 0xff6961)
    static let dangerSoft = Color.dynamic(0xc4161c, 0xff6961, alpha: 0.09, darkAlpha: 0.12)

    static let shadow = Color.dynamic(0x000000, 0x000000, alpha: 0.10, darkAlpha: 0.45)

    static let contentWidth: CGFloat = 700
    static let mono = Font.system(size: 12, design: .monospaced)
}

// MARK: 常用小部件

/// 扁平卡片：浅灰底、圆角；需要强调时加一圈细边。
struct CardBackground: ViewModifier {
    var fill: Color = Theme.s2
    var border: Color? = nil
    var radius: CGFloat = 12

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
    func card(fill: Color = Theme.s2, border: Color? = nil, radius: CGFloat = 12) -> some View {
        modifier(CardBackground(fill: fill, border: border, radius: radius))
    }
}

/// macOS 风格的按钮：默认（绿色实心）、普通（白底细边）、危险（红字）、安静（无底）。
struct PillButtonStyle: ButtonStyle {
    enum Kind { case primary, secondary, danger, quiet }
    var kind: Kind = .secondary
    var compact = false

    func makeBody(configuration: Configuration) -> some View {
        PushBody(configuration: configuration, kind: kind, compact: compact)
    }

    private struct PushBody: View {
        let configuration: ButtonStyleConfiguration
        let kind: Kind
        let compact: Bool
        @Environment(\.isEnabled) private var enabled
        @State private var hover = false

        var body: some View {
            configuration.label
                .font(.system(size: compact ? 12 : 13, weight: kind == .primary ? .medium : .regular))
                .lineLimit(1)
                .padding(.horizontal, compact ? 9 : 12)
                .frame(height: compact ? 22 : 26)
                .foregroundStyle(foreground)
                .background(background, in: RoundedRectangle(cornerRadius: 6, style: .continuous))
                .overlay {
                    if kind == .secondary || kind == .danger {
                        RoundedRectangle(cornerRadius: 6, style: .continuous).strokeBorder(Theme.line2, lineWidth: 0.5)
                    }
                }
                .shadow(color: kind == .quiet ? .clear : Theme.shadow.opacity(0.6), radius: 0.75, y: 0.5)
                .opacity(enabled ? (configuration.isPressed ? 0.75 : 1) : 0.45)
                .contentShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
                .onHover { hover = $0 }
        }

        private var foreground: Color {
            switch kind {
            case .primary: return Theme.onAccent
            case .secondary: return Theme.fg
            case .danger: return Theme.danger
            case .quiet: return hover ? Theme.fg : Theme.fg3
            }
        }

        private var background: Color {
            switch kind {
            case .primary: return hover ? Theme.accentHover : Theme.accent
            case .secondary, .danger: return Theme.button
            case .quiet: return hover ? Theme.hover : .clear
            }
        }
    }
}

/// 只有图标的小按钮（悬停时出现浅底）。
struct IconButtonStyle: ButtonStyle {
    var size: CGFloat = 26
    var tint: Color = Theme.fg3

    func makeBody(configuration: Configuration) -> some View {
        IconBody(configuration: configuration, size: size, tint: tint)
    }

    private struct IconBody: View {
        let configuration: ButtonStyleConfiguration
        let size: CGFloat
        let tint: Color
        @State private var hover = false
        @Environment(\.isEnabled) private var enabled

        var body: some View {
            configuration.label
                .font(.system(size: size * 0.5, weight: .regular))
                .foregroundStyle(hover ? Theme.fg : tint)
                .frame(width: size, height: size)
                .background(hover ? Theme.hover : .clear, in: RoundedRectangle(cornerRadius: 6, style: .continuous))
                .opacity(enabled ? (configuration.isPressed ? 0.7 : 1) : 0.4)
                .contentShape(Rectangle())
                .onHover { hover = $0 }
        }
    }
}

/// 小标签（风险等级、状态等）
@MainActor
struct Chip: View {
    var text: String
    var systemImage: String? = nil
    var tint: Color = Theme.fg3
    var fill: Color = Theme.s2

    var body: some View {
        HStack(spacing: 3) {
            if let systemImage { Image(systemName: systemImage).font(.system(size: 9, weight: .semibold)) }
            Text(text)
        }
        .font(.system(size: 11, weight: .medium))
        .foregroundStyle(tint)
        .padding(.horizontal, 7)
        .padding(.vertical, 2)
        .background(fill, in: Capsule())
    }
}

/// 描边小胶囊（「默认渠道」「当前选择」）
@MainActor
struct OutlinePill: View {
    var text: String
    var systemImage: String? = nil
    var tint: Color = Theme.fg3

    var body: some View {
        HStack(spacing: 3) {
            if let systemImage { Image(systemName: systemImage).font(.system(size: 9, weight: .bold)) }
            Text(text)
        }
        .font(.system(size: 11, weight: .medium))
        .foregroundStyle(tint)
        .padding(.horizontal, 8)
        .frame(height: 20)
        .overlay(Capsule().strokeBorder(tint == Theme.fg3 ? Theme.line2 : tint, lineWidth: 1))
    }
}

/// 三个跳动的小点：正在输入
@MainActor
struct TypingDots: View {
    var color: Color = Theme.fg4
    var size: CGFloat = 6

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

/// 一闪一闪的圆点（正在工作）
@MainActor
struct PulseDot: View {
    var color: Color = Theme.accent
    var size: CGFloat = 6
    @State private var on = false

    var body: some View {
        Circle()
            .fill(color)
            .frame(width: size, height: size)
            .opacity(on ? 0.3 : 1)
            .animation(.easeInOut(duration: 0.8).repeatForever(autoreverses: true), value: on)
            .onAppear { on = true }
    }
}

/// 细线转圈（运行中）
@MainActor
struct Spinner: View {
    var size: CGFloat = 14
    var tint: Color = Theme.fg3

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / 30)) { ctx in
            let angle = ctx.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 1) * 360
            ZStack {
                Circle().stroke(Theme.line2, lineWidth: 1.5)
                Circle().trim(from: 0, to: 0.25).stroke(tint, style: StrokeStyle(lineWidth: 1.5, lineCap: .round))
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
            .offset(y: shown ? 0 : 4)
            .onAppear { withAnimation(.easeOut(duration: 0.2)) { shown = true } }
    }
}

extension View {
    func rise() -> some View { modifier(Rise()) }
}

/// 设置页里的分组：白底圆角（系统设置的样式）。行之间用 RowDivider() 隔开。
@MainActor
struct FormGroup<Content: View>: View {
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(spacing: 0) { content() }
            .background(Theme.groupFill, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Theme.line, lineWidth: 0.5))
    }
}

@MainActor
struct RowDivider: View {
    var body: some View { Rectangle().fill(Theme.line).frame(height: 0.5).padding(.horizontal, 12) }
}

/// 设置分组里的一行：左边标题，右边内容
@MainActor
struct FormRow<Trailing: View>: View {
    var title: String
    @ViewBuilder var trailing: () -> Trailing

    var body: some View {
        HStack(spacing: 10) {
            Text(title).font(.system(size: 13)).foregroundStyle(Theme.fg)
            Spacer(minLength: 8)
            trailing()
        }
        .padding(.horizontal, 12)
        .frame(minHeight: 40)
    }
}

extension Theme {
    /// 设置窗口里分组的底色（窗口底是浅灰，分组是白色）
    static let groupFill = Color.dynamic(0xffffff, 0x2a2a2c)
    static let settingsBG = Color.dynamic(0xf5f5f6, 0x1e1e1e)
}
