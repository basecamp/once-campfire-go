#!/usr/bin/env node
// Start a disposable Go instance and run the shared Campfire browser flow unchanged.
import { mkdtemp, rm } from "node:fs/promises"
import { createServer } from "node:net"
import { spawn } from "node:child_process"
import { createRequire } from "node:module"
import { fileURLToPath } from "node:url"
import path from "node:path"
import { checkFrameCancellation } from "./frame-cancellation.mjs"

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..")
const verification = path.resolve(process.env.VERIFICATION_ROOT || path.join(root, "../once-campfire-verification"))
const require = createRequire(path.join(verification, "package.json"))
const { chromium } = require("playwright")
const work = await mkdtemp(path.join(root, ".cache/tmp/browser-"))
const socket = createServer()
await new Promise(resolve => socket.listen(0, "127.0.0.1", resolve))
const port = socket.address().port
await new Promise(resolve => socket.close(resolve))
const base = `http://127.0.0.1:${port}`
const server = spawn(process.env.GO_BINARY || path.join(root, "campfire"), ["server"], {
  cwd: root,
  env: { ...process.env, SECRET_KEY_BASE: "browser-test-only", DISABLE_SSL: "1", HTTP_PORT: "0", TARGET_BIND: "127.0.0.1", TARGET_PORT: String(port), CAMPFIRE_STORAGE_PATH: work, CAMPFIRE_DATABASE_PATH: path.join(work, "browser.sqlite3") },
  stdio: ["ignore", "ignore", "inherit"]
})
let browser
try {
  let ready = false
  for (let i = 0; i < 100; i++) {
    if (server.exitCode !== null) throw new Error("Server exited before becoming ready")
    try { ready = (await fetch(`${base}/up`)).ok } catch {}
    if (ready) break
    await new Promise(resolve => setTimeout(resolve, 50))
  }
  if (!ready) throw new Error(`Server did not start: ${base}`)
  if (process.argv.includes("--turbo-cancellation")) {
    // Focused regression for the port's intentional two-await Turbo override.
    browser = await chromium.launch({ headless: true, env: { ...process.env, TMPDIR: work } })
    await checkFrameCancellation(browser, base)
  } else {
    const flow = spawn(process.execPath, [path.join(verification, "browser/smoke.mjs"), "--base", base], { env: process.env, stdio: "inherit" })
    const code = await new Promise((resolve, reject) => { flow.once("error", reject); flow.once("exit", resolve) })
    if (code !== 0) throw new Error(`Shared browser flow exited ${code}`)
  }
} finally {
  await browser?.close()
  server.kill("SIGTERM")
  if (server.exitCode === null) await new Promise(resolve => server.once("exit", resolve))
  await rm(work, { recursive: true, force: true })
}
