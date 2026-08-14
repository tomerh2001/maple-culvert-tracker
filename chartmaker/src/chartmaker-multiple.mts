import { createCanvas } from "@napi-rs/canvas"

import { Chart, type ChartItem } from "chart.js"
import ChartDataLabels from "chartjs-plugin-datalabels"
import { fontFamily } from "./fontfamily.mts"

const dataColors = [
  "#ea5545",
  "#f46a9b",
  "#ef9b20",
  "#edbf33",
  "#ede15b",
  "#bdcf32",
  "#87bc45",
  "#27aeef",
  "#b33dc6",
] as const

export const chartmakerMultiple = (data: {
  labels: string[]
  dataPlots: { characterName: string; scores: (number | null)[] }[]
}): Buffer => {
  const width = data.labels.length <= 8 ? 1000 : 125 * data.labels.length
  let height = data.dataPlots.length * 10
  if (height < 600) height = 600
  const chartJSNodeCanvas = createCanvas(width, height)
  const ctx = chartJSNodeCanvas.getContext("2d")

  const chart = new Chart(ctx as unknown as ChartItem, {
    plugins: [ChartDataLabels as any],
    type: "line",
    data: {
      labels: data.labels,
      datasets: data.dataPlots.map((lineData, i) => {
        return {
          label: lineData.characterName,
          data: lineData.scores,
          datalabels: {
            align: "end",
            anchor: "end",
          },
          fill: true,
          borderColor: dataColors[i % dataColors.length],
          tension: 0.1,
          spanGaps: true,
        }
      }),
    },
    options: {
      plugins: {
        datalabels: {
          backgroundColor: function (context: any) {
            // https://chartjs-plugin-datalabels.netlify.app/guide/options.html#option-context
            return dataColors[context.datasetIndex % dataColors.length]
          },
          borderRadius: 4,
          color: "white",
          font: {
            weight: "bold",
            family: fontFamily,
          },
          // Skip labels for gap (null) points; round real scores as before.
          formatter: (value: number | null) =>
            value == null ? null : Math.round(value),
          padding: 6,
        },
      },
    },
  })
  const b = chartJSNodeCanvas.toBuffer("image/png")
  chart.destroy()
  return b
}
