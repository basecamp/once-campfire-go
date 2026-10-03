#!/usr/bin/env node
// Functional browser smoke test; does not replace the original screenshot parity suite.
import { mkdtemp, rm } from "node:fs/promises"
import { createServer } from "node:net"
import { spawn } from "node:child_process"
import { createRequire } from "node:module"
import { fileURLToPath } from "node:url"
import path from "node:path"

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..")
const require = createRequire(import.meta.url)
const { chromium } = require(path.resolve(process.env.RUST_ROOT || path.join(root, "../once-campfire-rust"), "parity/node_modules/playwright"))
const work = await mkdtemp(path.join(root, ".cache/tmp/browser-"))
const socket = createServer()
await new Promise(resolve => socket.listen(0, "127.0.0.1", resolve))
const port = socket.address().port
await new Promise(resolve => socket.close(resolve))
const base = `http://127.0.0.1:${port}`
let logs = "", browser
const server = spawn(path.join(root, "campfire"), ["server"], {
  cwd: root,
  env: { ...process.env, SECRET_KEY_BASE: "browser-test-only", DISABLE_SSL: "1", HTTP_PORT: "0", TARGET_BIND: "127.0.0.1", TARGET_PORT: String(port), CAMPFIRE_STORAGE_PATH: work, CAMPFIRE_DATABASE_PATH: path.join(work, "browser.sqlite3") },
  stdio: ["ignore", "pipe", "pipe"]
})
server.stdout.on("data", chunk => { logs += chunk })
server.stderr.on("data", chunk => { logs += chunk })
try {
  let ready = false
  for (let i = 0; i < 100; i++) {
    if (server.exitCode !== null) throw new Error(logs)
    try { ready = (await fetch(`${base}/up`)).ok } catch {}
    if (ready) break
    await new Promise(resolve => setTimeout(resolve, 50))
  }
  if (!ready) throw new Error(`Server did not start: ${logs}`)
  browser = await chromium.launch({ headless: true, env: { ...process.env, TMPDIR: work } })
  const context = await browser.newContext({ permissions: ["clipboard-read", "clipboard-write"] })
  const errors = []
  function watch(page) {
    if(process.env.DEBUG_BROWSER){page.on("response",async res=>{if(new URL(res.url()).pathname.startsWith("/rooms/")||res.status()>=400)console.error(res.status(),res.url(),(await res.text().catch(()=>"")).slice(0,500))});page.on("websocket",ws=>ws.on("framereceived",({payload})=>console.error("WS",String(payload).slice(0,700))))}
    page.on("pageerror", error => {errors.push(String(error));if(process.env.DEBUG_BROWSER)console.error(error)})
    page.on("console", msg => { if (msg.type() === "error") {errors.push(msg.text()); if (process.env.DEBUG_BROWSER) console.error(msg.text())} })
    return new Promise(resolve => page.on("websocket", socket => socket.on("framereceived", ({ payload }) => {
      if (JSON.parse(String(payload)).type === "confirm_subscription") resolve()
    })))
  }
  const first = await context.newPage()
  const firstSubscribed = watch(first)
  await first.goto(`${base}/first_run`)
  await first.locator('[name="user[name]"]').fill("Browser User")
  await first.locator('[name="user[email_address]"]').fill("browser@example.test")
  await first.locator('[name="user[password]"]').fill("browser test password")
  await Promise.all([first.waitForURL("**/rooms/*"), first.getByRole("button", { name: "Save" }).click()])
  const roomURL = first.url()
  const second = await context.newPage()
  const secondSubscribed = watch(second)
  await second.goto(first.url())
  await Promise.race([Promise.all([firstSubscribed, secondSubscribed]), new Promise((_, reject) => setTimeout(() => reject(new Error("subscription timeout")), 10000).unref())])
  await first.getByRole("textbox", { name: "Write a message", exact: true }).fill("Hello from the first tab")
  await first.getByRole("button", { name: "Send Message", exact: true }).click()
  await second.locator(".messages > .message[data-message-id]").filter({ hasText: "Hello from the first tab" }).waitFor()
  await second.getByRole("textbox", { name: "Write a message", exact: true }).fill("Reply from the second tab")
  await second.getByRole("button", { name: "Send Message", exact: true }).click()
  await first.locator(".messages > .message[data-message-id]").filter({ hasText: "Reply from the second tab" }).waitFor()
  await first.locator("lexxy-editor#message_body").evaluate(el => { el.value = '<img src=x onerror="window.injected=true"><b>safe browser text</b>' })
  await first.getByRole("button", { name: "Send Message", exact: true }).click()
  await second.locator(".messages > .message[data-message-id]").filter({ hasText: "safe browser text" }).waitFor()
  for (const page of [first, second]) {
    if (await page.locator(".messages > .message[data-message-id]").count() !== 3) throw new Error("duplicate or missing message")
    if (await page.evaluate(() => window.injected === true)) throw new Error("stored markup executed")
  }
  const messageID = await first.locator(".messages > .message[data-message-id]").filter({ hasText: "Hello from the first tab" }).getAttribute("id")
  const message = first.locator(`[id="${messageID}"]`)
  await message.locator("summary").click()
  await message.getByRole("button", { name: "Copy link", exact: true }).click()
  const copiedLink = await first.evaluate(() => navigator.clipboard.readText())
  const numericID = await message.getAttribute("data-message-id")
  if (copiedLink !== `${roomURL}/@${numericID}`) throw new Error(`Incorrect message permalink: ${copiedLink}`)
  await message.getByRole("link", { name: "Edit", exact: true }).click()
  await message.getByRole("textbox", { name: "Edit message", exact: true }).fill("Edited in the browser")
  await message.getByRole("button", { name: "Save changes", exact: true }).click()
  await second.locator(".messages > .message[data-message-id]").filter({ hasText: "Edited in the browser" }).waitFor()
  await first.goto(`${base}/searches?q=Edited`)
  await first.locator("#search-results > .message").filter({ hasText: "Edited in the browser" }).waitFor()
  await first.goto(`${base}/users/me/profile`)
  await first.locator('[name="user[bio]"]').fill("Browser profile update")
  await first.getByRole("button", { name: "Save changes", exact: true }).click()
  await first.waitForFunction(() => document.querySelector('[name="user[bio]"]').value === "Browser profile update")
  const transferURL = await first.locator("#session_transfer_url").inputValue()
  if (!transferURL.startsWith(`${base}/session/transfers/`)) throw new Error("Missing transfer URL")
  const qr = await context.request.get(base + await first.getByRole("link", { name: "Show auto-login QR code" }).getAttribute("href"))
  if (!qr.ok() || !qr.headers()["content-type"].includes("image/svg+xml")) throw new Error("QR code failed")
  await first.goto(`${base}/account/edit`)
  await first.locator('[name="account[name]"]').fill("Browser Campfire")
  await first.getByRole("button", { name: "Save changes", exact: true }).click()
  await first.waitForFunction(() => document.querySelector('[name="account[name]"]').value === "Browser Campfire")
  await first.goto(`${base}/rooms/opens/new`)
  await first.getByRole("textbox", { name: "Name this room", exact: true }).fill("Browser room")
  await Promise.all([first.waitForURL(/\/rooms\/\d+$/), first.getByRole("button", { name: "Save", exact: true }).click()])
  await second.locator("#shared_rooms a").filter({ hasText: "Browser room" }).waitFor()
  const newRoomURL = first.url()
  await first.getByRole("link", { name: "Settings for this room", exact: true }).click()
  await first.getByRole("textbox", { name: "Name this room", exact: true }).fill("Renamed browser room")
  await Promise.all([first.waitForURL(newRoomURL), first.getByRole("button", { name: "Save", exact: true }).click()])
  await second.locator("#shared_rooms a").filter({ hasText: "Renamed browser room" }).waitFor()
  await first.goto(`${base}/account/bots/new`)
  await first.getByPlaceholder("Name the bot", { exact: true }).fill("Browser bot")
  await Promise.all([first.waitForURL(`${base}/account/bots`), first.getByRole("button", { name: "Save changes", exact: true }).click()])
  const command = await first.getByRole("textbox", { name: "curl command for posting messages" }).first().inputValue()
  const botURL = command.slice(command.lastIndexOf(" ") + 1)
  const posted = await fetch(botURL, {method: "POST", body: "Hello from browser bot"})
  if (!posted.ok) throw new Error(`Bot post failed: ${posted.status}`)
  await first.getByRole("link", { name: "Edit Browser bot", exact: true }).click()
  await first.locator('[name="user[name]"]').fill("Renamed browser bot")
  await Promise.all([first.waitForURL(`${base}/account/bots`), first.getByRole("button", { name: "Save changes", exact: true }).click()])
  await first.getByRole("link", { name: "Edit Renamed browser bot", exact: true }).waitFor()
  await first.goto(`${base}/account/custom_styles/edit`)
  await first.locator('[name="account[custom_styles]"]').fill("body { --browser-test: verified; }")
  await Promise.all([first.waitForURL(`${base}/account/edit`), first.getByRole("button", { name: "Save changes", exact: true }).click()])
  if ((await first.evaluate(() => getComputedStyle(document.body).getPropertyValue("--browser-test"))).trim() !== "verified") throw new Error("Custom styles missing")
  const inviteURL = await first.locator("#invite_url").inputValue()
  const transferContext = await browser.newContext()
  const transferred = await transferContext.newPage()
  watch(transferred)
  await transferred.goto(transferURL)
  await transferred.waitForURL(/\/rooms\/\d+$/)
  await transferred.goto(`${base}/users/me/profile`)
  if (await transferred.locator('[name="user[email_address]"]').inputValue() !== "browser@example.test") throw new Error("Transfer signed in the wrong user")
  await transferContext.close()
  const joinedContext = await browser.newContext()
  const joined = await joinedContext.newPage()
  watch(joined)
  await joined.goto(inviteURL)
  await joined.locator('[name="user[name]"]').fill("Joined browser user")
  await joined.locator('[name="user[email_address]"]').fill("joined@example.test")
  await joined.locator('[name="user[password]"]').fill("joining test password")
  await Promise.all([joined.waitForURL(/\/rooms\/\d+$/), joined.getByRole("button", { name: "Save", exact: true }).click()])
  await joined.goto(`${base}/account/edit`)
  if (await joined.getByRole("link", { name: "Set up chat bots" }).count()) throw new Error("Member sees admin controls")
  await first.goto(roomURL)
  await first.getByRole("link", { name: "New Ping" }).click()
  const autocomplete = first.locator('[data-autocomplete-target="input"]')
  await autocomplete.fill("Joined browser")
  await first.locator("suggestion-option").getByText("Joined browser user", {exact: true}).click()
  await first.waitForFunction(() => document.querySelector('[data-autocomplete-target="select"]')?.selectedOptions.length === 1)
  if (process.env.DEBUG_BROWSER) console.error(await first.locator('[data-autocomplete-target="select"]').evaluate(el=>({html:el.outerHTML,value:el.value,valid:el.validity.valid,form:el.form?.outerHTML})))
  await Promise.all([first.waitForURL(url => /\/rooms\/\d+$/.test(url.pathname) && url.href !== roomURL), first.getByRole("button", {name: "Start Ping", exact: true}).click()])
  await first.locator("#nav").getByRole("heading", {name: "Ping with Joined browser user", exact: true}).waitFor()
  await joinedContext.close()
  if (errors.length) throw new Error(errors.join("\n"))
  console.log("PASS: setup, two-tab live messaging, duplicate suppression, and stored-markup safety, copying message permalinks, editing, search, profile/account updates, QR codes, live room creation/renaming, bots, custom styles, session transfers, joining, and autocomplete-started direct pings in Chromium.")
} catch (error) {
 console.error(logs)
 for (const page of browser?.contexts()[0]?.pages() || []) {console.error(await page.locator("body").innerText());console.error(await page.locator("#composer").getAttribute("outerHTML").catch(()=>""))}
 throw error
} finally {
  await browser?.close()
  server.kill("SIGTERM")
  if (server.exitCode === null) await new Promise(resolve => server.once("exit", resolve))
  await rm(work, { recursive: true, force: true })
}
