import { createServer } from "node:http"

// Exercise the application's served Turbo bytes with a real, unfinished HTTP body.
// Playwright's fulfilled routes cannot reproduce cancellation after headers arrive.
export async function checkFrameCancellation(browser, base) {
  const manifestResponse = await fetch(`${base}/assets/.manifest.json`)
  if (!manifestResponse.ok) throw new Error("Cannot read application asset manifest")
  const manifest = await manifestResponse.json()
  const response = await fetch(`${base}/assets/${manifest["turbo.js"].digested_path}`)
  if (!response.ok) throw new Error("Cannot read application Turbo asset")
  const turbo = await response.text()
  let cancelled = 0
  const server = createServer((request, response) => {
    const url = new URL(request.url, "http://localhost")
    if (url.pathname === "/turbo.js") {
      response.writeHead(200, { "Content-Type": "text/javascript" })
      response.end(turbo)
    } else if (url.pathname === "/slow") {
      response.writeHead(Number(url.searchParams.get("status")), { "Content-Type": "text/html" })
      response.write('<turbo-frame id="fixture">unfinished')
      response.once("close", () => { if (!response.writableEnded) cancelled++ })
      // Deliberately leave the body unfinished until the browser cancels it.
    } else if (url.pathname === "/replacement") {
      response.writeHead(Number(url.searchParams.get("status") || 200), { "Content-Type": "text/html" })
      response.end('<turbo-frame id="fixture">replacement</turbo-frame>')
    } else {
      response.writeHead(200, { "Content-Type": "text/html" })
      response.end(`<!doctype html><html><body>
        <script type="module">
          import { Turbo } from "/turbo.js"
          window.Turbo = Turbo
          document.addEventListener("turbo:before-fetch-response", event => {
            if (event.target.id === "fixture") window.receivedStatus = event.detail.fetchResponse.statusCode
          })
        </script>
        <turbo-frame id="fixture"></turbo-frame>
      </body></html>`)
    }
  })
  const context = await browser.newContext()
  try {
    await new Promise(resolve => server.listen(0, "127.0.0.1", resolve))
    const origin = `http://127.0.0.1:${server.address().port}`
    const page = await context.newPage()
    const errors = [], consoleErrors = []
    page.on("pageerror", error => errors.push(String(error)))
    page.on("console", message => {
      if (message.type() === "error") consoleErrors.push({ text: message.text(), url: message.location().url })
    })
    for (const status of [200, 422]) {
      await page.goto(origin)
      await page.waitForFunction(() => window.Turbo)
      await page.evaluate(status => {
        document.getElementById("fixture").src = `/slow?status=${status}`
      }, status)
      await page.waitForFunction(status => window.receivedStatus === status, status)
      await page.evaluate(() => { document.getElementById("fixture").src = "/replacement" })
      await page.waitForFunction(() => document.getElementById("fixture").textContent === "replacement")
      await page.waitForLoadState("networkidle")
      if (errors.length) throw new Error(`Cancelled ${status} frame body: ${errors.join("\n")}`)
    }
    if (cancelled !== 2) throw new Error(`Expected two cancelled body reads, got ${cancelled}`)

    // The existing handler must still report and reject non-cancellation errors.
    // A delegate is the public FetchRequest boundary, not a patched browser API.
    for (const status of [200, 422]) {
      const result = await page.evaluate(async status => {
        const failure = new Error("delegate failed")
        let reported = false, finished = 0, succeeded = false, failed = false
        const delegate = {
          prepareRequest() {},
          requestStarted() {},
          async requestSucceededWithResponse() { succeeded = true; throw failure },
          async requestFailedWithResponse() { failed = true; throw failure },
          requestErrored(_request, error) { reported = error === failure },
          requestFinished() { finished++ }
        }
        const request = new window.Turbo.FetchRequest(delegate, "get", new URL(`/replacement?status=${status}`, location.href))
        let rejected = false
        try { await request.perform() } catch (error) { rejected = error === failure }
        return { reported, rejected, finished, succeeded, failed }
      }, status)
      if (!result.reported || !result.rejected || result.finished !== 1 ||
          result.succeeded !== (status === 200) || result.failed !== (status === 422)) {
        throw new Error(`Non-abort ${status} delegate error was lost: ${JSON.stringify(result)}`)
      }
    }
    await page.waitForLoadState("networkidle")
    if (errors.length) throw new Error(errors.join("\n"))
    // These two deliberately unsuccessful HTTP fixtures produce Chromium resource
    // diagnostics. Require both, at their exact fixture URLs; accept no other errors.
    const expectedDiagnostics = ["/slow?status=422", "/replacement?status=422"]
    if (consoleErrors.length !== expectedDiagnostics.length || expectedDiagnostics.some(path =>
      !consoleErrors.some(error => error.url === origin + path && error.text.includes("422")))) {
      throw new Error(`Unexpected fixture diagnostics: ${JSON.stringify(consoleErrors)}`)
    }
    console.log("PASS: cancelled success/failure frame bodies recover; other delegate errors propagate.")
  } finally {
    await context.close()
    server.closeAllConnections()
    await new Promise(resolve => server.close(resolve))
  }
}
