import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui
import "Model.js" as Model

// OmaWhats in the bar: the glyph, the unread count, and behind one click the
// power switch, the pairing QR, and the latest conversations.
//
// The daemon runs only while switched on. Switched off, the glyph dims and the
// list shows what was stored the last time it ran.
Panel {
  id: root
  moduleName: "megamvb.omawhats"
  ipcTarget: "megamvb.omawhats"
  manageIpc: false

  // "hero" | "chats" | "footer"
  property string focusSection: "hero"
  property int rowIndex: 0
  property bool cursorActive: false
  property double nowMs: Date.now()

  readonly property color foreground: bar ? bar.foreground : Color.foreground
  readonly property color urgent: bar ? bar.urgent : Color.urgent
  readonly property color accent: Color.accent
  readonly property color dim: Qt.darker(foreground, 1.55)
  readonly property string fontFamily: bar ? bar.fontFamily : Style.font.family

  readonly property int chatCount: Math.max(3, Math.min(20, parseInt(setting("panelChats", 8), 10) || 8))
  readonly property var rows: wa.chats.slice(0, chatCount)
  readonly property bool alarmed: Model.isAlarm(wa.daemonState, wa.live)
  readonly property bool showBadge: setting("showUnreadCount", true) !== false
  readonly property string badge: showBadge ? Model.unreadBadge(wa.unread) : ""
  readonly property string barText: Model.GLYPH_WHATSAPP + (badge !== "" ? "  " + badge : "")
  readonly property color barIconColor: wa.online ? barForeground : Qt.darker(barForeground, 1.55)
  readonly property string errorText: wa.startError !== "" ? wa.startError : (wa.live ? wa.daemonState.error : "")

  readonly property string tooltip: {
    if (!wa.live) return "OmaWhats — off" + (wa.unread > 0 ? " · " + wa.unread + " unread" : "")
    var t = "OmaWhats — " + Model.stateLabel(wa.daemonState, wa.live)
    if (wa.unread > 0) t += " · " + wa.unread + " unread"
    return t
  }

  // action: "" | "new" | "logout" | "help" — see Chat.qml. asWindow opens it
  // as an ordinary window; otherwise an open client keeps its mode.
  function openClient(chat, action, asWindow) {
    var p = { chat: chat || "", action: action || "" }
    if (asWindow) p.window = true
    var payload = JSON.stringify(p)
    Quickshell.execDetached(["omarchy-shell", "shell", "summon", "megamvb.omawhats", payload])
    close()
  }

  function ensureCursor() {
    if (rows.length === 0 && focusSection === "chats") focusSection = "hero"
    if (rowIndex >= rows.length) rowIndex = Math.max(0, rows.length - 1)
    if (rowIndex < 0) rowIndex = 0
  }

  function moveCursor(dx, dy) {
    cursorActive = true
    ensureCursor()
    if (dy === 0) return
    if (focusSection === "hero") {
      if (dy > 0) {
        if (rows.length > 0) setRowCursor(0)
        else focusSection = "footer"
      }
      return
    }
    if (focusSection === "footer") {
      if (dy < 0) {
        if (rows.length > 0) setRowCursor(rows.length - 1)
        else setHeroCursor()
      }
      return
    }
    if (dy < 0 && rowIndex === 0) { setHeroCursor(); return }
    if (dy > 0 && rowIndex === rows.length - 1) { focusSection = "footer"; scrollFooterIntoView(); return }
    rowIndex = Math.max(0, Math.min(rows.length - 1, rowIndex + dy))
    scrollCursorIntoView()
  }

  function setHeroCursor() {
    cursorActive = true
    focusSection = "hero"
    if (panelFlick) panelFlick.contentY = 0
  }

  function setRowCursor(index) {
    cursorActive = true
    focusSection = "chats"
    rowIndex = index
    scrollCursorIntoView()
  }

  function activateCursor() {
    ensureCursor()
    if (focusSection === "hero") wa.togglePower()
    else if (focusSection === "footer") openClient("")
    else if (rows.length > 0) openClient(rows[rowIndex].jid)
  }

  function scrollItemIntoView(item) {
    if (!panelFlick || !item) return
    Qt.callLater(function() {
      if (!item) return
      var margin = Style.space(6)
      var point = item.mapToItem(panelFlick.contentItem, 0, 0)
      var top = point.y
      var bottom = top + item.height
      var maxY = Math.max(0, panelFlick.contentHeight - panelFlick.height)
      if (top < panelFlick.contentY + margin) panelFlick.contentY = Math.max(0, top - margin)
      else if (bottom > panelFlick.contentY + panelFlick.height - margin) panelFlick.contentY = Math.min(maxY, bottom + margin - panelFlick.height)
    })
  }

  function scrollCursorIntoView() {
    if (focusSection === "chats" && chatColumn && rowIndex >= 0 && rowIndex < chatColumn.children.length)
      scrollItemIntoView(chatColumn.children[rowIndex])
  }

  function scrollFooterIntoView() { scrollItemIntoView(footerButton) }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  onOpenedChanged: if (opened) {
    cursorActive = false
    focusSection = "hero"
    rowIndex = 0
    nowMs = Date.now()
    if (panelFlick) panelFlick.contentY = 0
    wa.refresh()
    Qt.callLater(function() { keyCatcher.forceActiveFocus() })
  }

  Service {
    id: wa
    settings: root.settings
  }

  Connections {
    target: wa
    function onChatsChanged() { root.ensureCursor() }
  }

  IpcHandler {
    target: root.ipcTarget
    function open(): void { root.open() }
    function close(): void { root.close() }
    function show(): void { root.open() }
    function hide(): void { root.close() }
    function toggle(): void { root.toggle() }
    function start(): string { wa.start(); return "ok" }
    function stop(): string { wa.stop(); return "ok" }
    function power(): string { wa.togglePower(); return "ok" }
    function client(): string { root.openClient(""); return "ok" }
    function window(): string { root.openClient("", "", true); return "ok" }
    function newChat(): string { root.openClient("", "new"); return "ok" }
    function logout(): string { root.openClient("", "logout"); return "ok" }
    function unread(): string { return String(wa.unread) }
    function status(): string { return root.tooltip }
    function debug(): string { return wa.debugTrace }
  }

  WidgetButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: root.barText
    labelVisible: true
    hasVisualContent: text !== ""
    active: root.alarmed
    useActiveColor: true
    foreground: root.barIconColor
    tooltipText: root.tooltip

    onPressed: function(buttonCode) {
      if (buttonCode === Qt.RightButton) root.openClient("")
      else if (buttonCode === Qt.MiddleButton) wa.togglePower()
      else root.toggle()
    }
  }

  Timer {
    interval: 30000
    running: root.opened
    repeat: true
    onTriggered: root.nowMs = Date.now()
  }

  KeyboardPanel {
    id: panel
    anchorItem: button
    owner: root
    bar: root.bar
    open: root.opened
    focusTarget: keyCatcher
    contentWidth: panel.fittedContentWidth(Style.space(400))
    contentHeight: panel.fittedContentHeight(column.implicitHeight, Style.space(620))

    PanelKeyCatcher {
      id: keyCatcher
      anchors.fill: parent
      onMoveRequested: function(dx, dy) {
        if (!root.cursorActive) { root.cursorActive = true; return }
        root.moveCursor(dx, dy)
      }
      onActivateRequested: if (root.cursorActive) root.activateCursor()
      onCloseRequested: root.close()
      onTabRequested: function(direction) { root.switchPanel(direction) }
      onTextKey: function(t) {
        if (t === "o" || t === "O") root.openClient("")
        else if (t === "w" || t === "W") root.openClient("", "", true)
        else if (t === "n" || t === "N") root.openClient("", "new")
        else if (t === "p" || t === "P") wa.togglePower()
        else if (t === "r" || t === "R") wa.refresh()
        else if (t === "?") root.openClient("", "help")
        else if (t === "l" || t === "L") { if (wa.daemonState.paired) root.openClient("", "logout") }
      }

      Flickable {
        id: panelFlick
        anchors.fill: parent
        contentWidth: width
        contentHeight: column.implicitHeight
        clip: true
        boundsBehavior: Flickable.StopAtBounds
        flickableDirection: Flickable.VerticalFlick
        interactive: contentHeight > height
        ScrollBar.vertical: ScrollBar { policy: ScrollBar.AsNeeded }

        Column {
          id: column
          width: panelFlick.width
          spacing: Style.space(12)

          Item {
            id: header
            width: parent.width
            implicitHeight: hero.implicitHeight
            readonly property bool ringVisible: root.cursorActive && root.focusSection === "hero"

            PanelHero {
              id: hero
              width: parent.width
              title: "OmaWhats"
              meta: wa.starting ? "Turning on…" : (wa.stopping ? "Turning off…" : Model.stateLabel(wa.daemonState, wa.live))
              detail: Model.stateDetail(wa.daemonState, wa.live)
              foreground: root.foreground
              fontFamily: root.fontFamily
              iconOpacity: wa.online ? 1.0 : 0.55
              iconComponent: Component {
                Text {
                  text: Model.GLYPH_WHATSAPP
                  color: root.alarmed ? root.urgent : root.foreground
                  font.family: root.fontFamily
                  font.pixelSize: Style.font.display
                }
              }

              trailingControl: Component {
                PanelActionButton {
                  iconText: Model.GLYPH_POWER
                  tooltipText: wa.live ? "Turn off (P) — disconnects and stops the daemon" : "Turn on (P)"
                  foreground: wa.live ? Color.accent : hero.foreground
                  fontFamily: hero.fontFamily
                  hasCursor: header.ringVisible
                  enabled: !wa.starting && !wa.stopping
                  onClicked: wa.togglePower()
                }
              }
            }
          }

          Text {
            visible: root.errorText !== ""
            width: parent.width
            text: root.errorText
            color: root.urgent
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
            wrapMode: Text.WordWrap
          }

          // ---- pairing

          Column {
            visible: Model.needsPairing(wa.daemonState, wa.live)
            width: parent.width
            spacing: Style.space(10)

            QrCode {
              visible: wa.daemonState.state === "pairing"
              anchors.horizontalCenter: parent.horizontalCenter
              width: Math.min(parent.width, Style.space(260))
              height: width
              rows: wa.daemonState.qr
            }

            Text {
              width: parent.width
              text: wa.daemonState.state === "pairing"
                ? "On your phone: WhatsApp → Settings → Linked devices → Link a device, and point it at this code."
                : "Generate a new QR code to link this computer."
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.bodySmall
              wrapMode: Text.WordWrap
              horizontalAlignment: Text.AlignHCenter
            }

            Button {
              visible: wa.daemonState.state !== "pairing"
              anchors.horizontalCenter: parent.horizontalCenter
              iconText: Model.GLYPH_QR
              text: "Generate new QR code"
              foreground: root.foreground
              fontFamily: root.fontFamily
              onClicked: wa.pair()
            }
          }

          // ---- stopped and never paired

          Text {
            visible: !wa.live && !wa.daemonState.paired && !wa.starting
            width: parent.width
            text: "Turn OmaWhats on to get a QR code and link your account."
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: Style.font.body
            wrapMode: Text.WordWrap
            horizontalAlignment: Text.AlignHCenter
          }

          PanelSeparator {
            visible: root.rows.length > 0
            foreground: root.foreground
          }

          Column {
            width: parent.width
            spacing: Style.space(8)
            visible: root.rows.length > 0

            PanelSectionHeader {
              text: wa.live ? "CHATS" : "CHATS · STORED"
              foreground: root.foreground
              fontFamily: root.fontFamily
            }

            Column {
              id: chatColumn
              width: parent.width
              spacing: Style.space(4)

              Repeater {
                model: root.rows
                ChatRow {
                  required property var modelData
                  required property int index
                  width: chatColumn.width
                  chat: modelData
                  rowIndex: index
                }
              }
            }
          }

          Row {
            width: parent.width
            spacing: Style.space(6)
            visible: wa.daemonState.paired || root.rows.length > 0

            Button {
              id: footerButton
              width: parent.width - windowButton.width - parent.spacing
              iconText: Model.GLYPH_WHATSAPP
              text: "Open OmaWhats (O)"
              foreground: root.foreground
              fontFamily: root.fontFamily
              hasCursor: root.cursorActive && root.focusSection === "footer"
              onClicked: root.openClient("")
            }
            // The same client as an ordinary window instead of the popup.
            Button {
              id: windowButton
              iconText: Model.GLYPH_WINDOW
              text: "Window (W)"
              tooltipText: "Open OmaWhats in a normal window"
              foreground: root.foreground
              fontFamily: root.fontFamily
              onClicked: root.openClient("", "", true)
            }
          }

          Row {
            anchors.horizontalCenter: parent.horizontalCenter
            spacing: Style.space(6)
            visible: wa.daemonState.paired || root.rows.length > 0

            Button {
              iconText: Model.GLYPH_NEW_CHAT
              text: "New chat (N)"
              foreground: root.foreground
              fontFamily: root.fontFamily
              fontSize: Style.font.bodySmall
              onClicked: root.openClient("", "new")
            }
            Button {
              iconText: Model.GLYPH_KEYBOARD
              text: "Shortcuts (?)"
              foreground: root.foreground
              fontFamily: root.fontFamily
              fontSize: Style.font.bodySmall
              onClicked: root.openClient("", "help")
            }
            Button {
              visible: wa.daemonState.paired
              iconText: Model.GLYPH_LOGOUT
              text: "Log out…"
              foreground: root.foreground
              fontFamily: root.fontFamily
              fontSize: Style.font.bodySmall
              onClicked: root.openClient("", "logout")
            }
          }
        }
      }
    }
  }

  component ChatRow: CursorSurface {
    id: chatRow
    property var chat: null
    property int rowIndex: 0
    readonly property int unreadCount: chat ? (parseInt(chat.unread, 10) || 0) : 0
    readonly property bool manualUnread: Model.isManualUnread(chat)

    hasCursor: root.cursorActive && root.focusSection === "chats" && root.rowIndex === rowIndex
    foreground: root.foreground
    implicitHeight: rowContent.implicitHeight + Style.spacing.rowPaddingX

    MouseArea {
      anchors.fill: parent
      hoverEnabled: true
      cursorShape: Qt.PointingHandCursor
      onEntered: root.setRowCursor(chatRow.rowIndex)
      onClicked: root.openClient(chatRow.chat.jid)
    }

    RowLayout {
      anchors.left: parent.left
      anchors.right: parent.right
      anchors.verticalCenter: parent.verticalCenter
      anchors.leftMargin: Style.space(10)
      anchors.rightMargin: Style.space(10)
      spacing: Style.space(8)

      Text {
        text: chatRow.chat && chatRow.chat.group ? Model.GLYPH_GROUP : Model.GLYPH_PERSON
        color: root.foreground
        opacity: chatRow.unreadCount > 0 ? 1.0 : 0.5
        font.family: root.fontFamily
        font.pixelSize: Style.font.icon
        Layout.alignment: Qt.AlignVCenter
      }

      ColumnLayout {
        id: rowContent
        Layout.fillWidth: true
        spacing: Style.space(1)

        Text {
          Layout.fillWidth: true
          text: Model.chatName(chatRow.chat)
          color: root.foreground
          opacity: chatRow.unreadCount > 0 ? 1.0 : 0.75
          font.family: root.fontFamily
          font.pixelSize: Style.font.body
          font.bold: chatRow.unreadCount > 0
          elide: Text.ElideRight
          maximumLineCount: 1
        }

        Text {
          Layout.fillWidth: true
          text: Model.chatPreview(chatRow.chat)
          color: root.dim
          font.family: root.fontFamily
          font.pixelSize: Style.font.caption
          elide: Text.ElideRight
          maximumLineCount: 1
          textFormat: Text.PlainText
        }
      }

      ColumnLayout {
        spacing: Style.space(2)
        Layout.alignment: Qt.AlignVCenter

        Text {
          Layout.alignment: Qt.AlignRight
          text: Model.listTime(chatRow.chat ? chatRow.chat.ts : 0, root.nowMs)
          color: chatRow.unreadCount > 0 ? root.accent : root.dim
          font.family: root.fontFamily
          font.pixelSize: Style.font.caption
        }

        Rectangle {
          Layout.alignment: Qt.AlignRight
          visible: chatRow.unreadCount > 0
          implicitWidth: Math.max(height, countText.implicitWidth + Style.space(8))
          implicitHeight: countText.implicitHeight + Style.space(2)
          radius: height / 2
          color: root.accent

          Text {
            id: countText
            anchors.centerIn: parent
            // By hand: a dot, since the count behind it means nothing.
            text: chatRow.manualUnread ? "" : Model.unreadBadge(chatRow.unreadCount)
            color: Color.background
            font.family: root.fontFamily
            font.pixelSize: Style.font.caption
            font.bold: true
          }
        }
      }
    }
  }
}
