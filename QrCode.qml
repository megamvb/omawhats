import QtQuick

// Draws the pairing QR from the daemon's rows of "0"/"1". Always dark on
// white with a quiet zone, whatever the theme: phone cameras are tuned for
// that and a themed QR can fail to scan.
Rectangle {
  id: root

  property var rows: []
  readonly property int modules: rows && rows.length ? rows.length : 0
  readonly property int quiet: 3

  color: "white"
  radius: 4

  onRowsChanged: canvas.requestPaint()

  Canvas {
    id: canvas
    anchors.fill: parent
    antialiasing: false
    onWidthChanged: requestPaint()
    onHeightChanged: requestPaint()
    onPaint: {
      var ctx = getContext("2d")
      ctx.reset()
      ctx.fillStyle = "white"
      ctx.fillRect(0, 0, width, height)
      var n = root.modules
      if (n === 0) return
      var total = n + root.quiet * 2
      // Whole-pixel modules so no row blurs into its neighbour.
      var cell = Math.floor(Math.min(width, height) / total)
      if (cell < 1) return
      var offX = Math.floor((width - cell * n) / 2)
      var offY = Math.floor((height - cell * n) / 2)
      ctx.fillStyle = "black"
      for (var y = 0; y < n; y++) {
        var row = String(root.rows[y])
        for (var x = 0; x < row.length; x++) {
          if (row.charAt(x) === "1") ctx.fillRect(offX + x * cell, offY + y * cell, cell, cell)
        }
      }
    }
  }
}
