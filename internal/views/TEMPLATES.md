# Templates

The contract for porting reference/crates/views/templates to quicktemplate. Every template except
the four layouts has a .qtpl here with its final signature and an empty body (`TODO: body`); the
view models, page types and helpers they use are in the .go files of the same module
(messages.go, rooms.go, users.go, ...). Keep this file in step with the signatures.

## Conventions

### Names

- Go name: bin/askama2qtpl's `func_name`, the CamelCase of the template path split on `/`, `_` and
  `.`, `.html` dropped: `rooms/show.html` → `RoomsShow`, `messages/create.turbo_stream.html` →
  `MessagesCreateTurboStream`, `pwa/manifest.json` → `PwaManifestJson`.
- File: the path without `.html`, `/` and `.` as `_`, a partial's leading `_` dropped (as the
  existing `layouts_lightbox.qtpl`): `messages/_message.html` → `messages_message.qtpl`. That is
  as unique as the Go names (which must be anyway); each file's first line names its template.

### Pages and funcs

- A template that `extends` a layout is a page: a Go struct embedding `PageBase` with the Rust
  struct's fields (`Ctx *ViewContext` and the view models, exported), `PageTitle()`/`BodyClass()`
  as the Rust `impl Page` (`*string`, nil = None), and one method per block it defines,
  `{% func (p *RoomsShow) Content() %}` in its .qtpl (qtc makes `StreamContent`, which `Page`
  needs). Blocks it doesn't define are PageBase's empty ones. The caller renders it in
  `LayoutsApplication(ctx, page)` or `LayoutsTurboRailsFrame(ctx, page)`.
- Everything else is a func: `ctx *ViewContext` first when the template uses ctx, then the Rust
  struct's fields in order. Partials that are only included (no Rust struct) take the includer's
  variables they use.
- rooms/layouts/_new and _edit are askama parent templates with a `room_form` block. In Go they
  are pages, `RoomsLayoutsNew`/`RoomsLayoutsEdit`, whose Content renders `{%= p.Page.RoomForm() %}`
  (`Page RoomFormPage`, an interface with `StreamRoomForm`). The pages extending them
  (RoomsOpensNew, RoomsOpensEdit, RoomsClosedsNew, RoomsClosedsEdit) embed them, add `Form`,
  define only `RoomForm`, and are built with `NewRoomsOpensNew(ctx, form)` etc., which point
  `Page` back at the page. askama2qtpl names the block `Room_form`; it is `RoomForm`.

### Types

- `Option<T>` → `*T` (nil = None); `Vec<T>` → `[]T`; `String`/`&str` → `string`; `i64` → `int64`;
  `u32` → `uint32`; `jiff::Timestamp` → `time.Time`; `h::Html` and `|safe` strings → `HTML`.
- Struct parameters are pointers (`*MessageView`, `*UserSummary`) whether Rust borrows or owns
  them; page fields keep the Rust struct's shape (`Show *RoomShowView` for `&ShowView`,
  `User UserSummary` for an owned `UserSummary`). `MessageItem`, `SidebarDirectItem`, `RoomKind`
  and `HTML` go by value.
- Rust enums with data are Go interfaces closed by an unexported marker method, one type per
  variant, used with type switches:
  `MessageContent` = `MessageText{HTML}` | `*SoundView` | `*AttachmentView` | `MessageUnrenderable{}`;
  `AttachmentPreview` = `AttachmentVideo{PosterURL}` | `AttachmentImage{ThumbURL}` | `AttachmentFile{}`.
  Templates don't switch on them: they call `message.IsUnrenderable()`, `message.Attachment()`
  (nil = None), `MessagePresentation`, `AttachmentPresentation`.
- Fieldless enums are uint8 constants: `RoomKind` (RoomKindOpen, RoomKindClosed, RoomKindDirect;
  `ParamKey()`, `IsDirect()`), `Role` (RoleMember, RoleAdministrator, RoleBot; `String()` is
  `as_str`), `Status` (StatusActive, StatusDeactivated, StatusBanned).
- The cached-or-view enums `MessageItem` and `SidebarDirectItem` are structs with unexported
  fields, built with `MessageItemFragment(clientMessageID, roomID, fragment)` /
  `MessageItemView(view)` and `SidebarDirectItemFragment(fragment)` / `SidebarDirectItemView(view)`;
  methods `DomID(prefix)`, `RoomID()` (MessageItem), `Fragment()`, `View()`.
- Renamed for the flat package: `messages::EditView` → `MessageEditView`, `rooms::ShowView` →
  `RoomShowView`, `searches::IndexView` → `SearchIndexView`, `messages::json::UserJson` →
  `UserJSON` (and `*Json` → `*JSON`), `autocompletable::UserJson` → `AutocompletableUserJSON`,
  `users::MentionUser`'s Deref → embedding `UserSummary`, `EmojiHelper::REACTIONS` → `Reactions`
  (`[]Reaction{Character, Title}`), `sessions::ALLOW_BROWSER_VERSIONS` → `AllowBrowserVersions`
  (`[]BrowserVersion{Browser, Version}`), `pwa::SERVICE_WORKER_JS` → `ServiceWorkerJS`.

### Rust expressions in Go

- `ctx.asset(x)` → `ctx.Asset(x)`, `ctx.url(x)` → `ctx.URL(x)`, `ctx.can_administer()` →
  `ctx.CanAdminister()`, `ctx.is_current_user(id)` → `ctx.IsCurrentUser(id)`, `ctx.platform.x` →
  `ctx.Platform.X`, `ctx.account.x` → `ctx.Account.X`, `ctx.flash_alert.is_some()` →
  `ctx.FlashAlert != nil`.
- `h::routes::x(...)` → `RouteX(...)` (routes.go); `h::x(...)` → the helper of the same name
  (helpers_*.go): `h::attrs()` → `NewAttrs()`, builder methods CamelCased (`type_` → `Type`,
  `aria_hidden()` → `AriaHidden()`, `attr_opt` → `AttrOpt`), `h::form_with(url)` → `FormWith(url)`
  (and `form.text_field(...)` → `form.TextField(...)` etc.), `h::dom_id(m, id, Some(p))` →
  `DomID(m, id, p)`, `h::account_logo_tag(ctx, None/Some(s))` → `AccountLogoTag(ctx, ""/s)`,
  `h::sidebar_turbo_frame_tag(Some(src), "")` → `SidebarTurboFrameTag(Ptr(src), "")`.
- Output: `{{ s }}` → `{%s s %}` (ERB escaping); integers → `{%dl n %}` (int64) / `{%d n %}`;
  helpers' `HTML` and `|safe` → `{%s= string(h) %}`.
- `{% let x = e %}` → `{% code x := e %}`; `if let Some(x) = opt` → `{% if opt != nil %}` and
  `*opt`; `for x in xs` → `{% for i := range xs %}` with `&xs[i]` where a pointer is wanted.
- Filter blocks: askama2qtpl emits `{% code views.BeginCapture(qw422016) %}...{% code captureN :=
  views.EndCapture(qw422016) %}{%s= RX(filter(args), captureN) %}`. Drop `views.` (same package)
  and call the block helper with the content first (helpers_filters.go): `link_to` → `LinkToBlock`,
  `button_to` → `ButtonToBlock`, `form_with(form)` → `FormWithBlock(content, form)`, `button` →
  `Button`, `turbo_frame_tag` → `TurboFrameTag`, `link_to_room` → `LinkToRoom`,
  `link_to_zoom_qr_code` → `LinkToZoomQrCode`, `button_to_copy_to_clipboard` →
  `ButtonToCopyToClipboard`, `web_share_session_button` → `WebShareSessionButton`,
  `user_filter_menu_tag` → `UserFilterMenuTag`, `sidebar_turbo_frame_tag` →
  `SidebarTurboFrameTagBlock`, `room_form` → `RoomForm` (rooms.go).
- `{% include %}` → askama2qtpl emits `{%= Name(RX_ARGS) %}`; the arguments are listed below
  under "Included by"/"Includes".
- `self.m()` on a page → `p.M()`. On a non-page template the Rust method is a Go method of its
  view model: `InvolvementShow::button` → `involvement.Button(ctx)`.

### Cached fragments and recording

- `crate::messages::cached_message_item(ctx, m)` → `{%= Fragment(CachedMessageItem(ctx, m)) %}`:
  recorded, as the reference hands it to the recorder.
- `crate::messages::cached_message(ctx, m)` → `{%s= CachedMessage(ctx, m).HTML %}`,
  `crate::messages::cached_boost(ctx, b)` → `{%s= CachedBoost(ctx, b).HTML %}`,
  `crate::users::cached_direct_room(ctx, item)` → `{%s= CachedDirectRoomItem(ctx, item).HTML %}`:
  copied as text, as the reference doesn't hand these over. `{%= Fragment(f) %}` records a
  fragment of 1 KB or more, which changes the page's ETag (parts.go), so follow the reference.
- Templates that capture (filter blocks) or write fragments must render into a views.Writer
  (Render, RenderString, RenderSize.Render), never through the generated `X() string`/`WriteX`:
  writerOf panics otherwise. The cached partials (messages/_message, messages/boosts/_boost,
  users/sidebars/rooms/_direct) render through renderFragment on a cache miss.
- The fragment keys' template digests (messageDigest, boostDigest, directRoomDigest) hash the
  embedded .qtpl sources, so filling a body changes them, as editing a template does in the
  reference.

### Whitespace

askama drops one trailing newline from every template file and applies `{%-`/`-%}`/`{#- -#}`;
askama2qtpl's output already has both applied. Paste its block or func body, translate the
`RX(...)` expressions, and compare with the reference's goldens (reference/crates/views/tests:
golden/, messages_views.rs, rooms_views.rs, searches_views.rs, parity_a.rs).

### Not templates

- pwa/service_worker.js is embedded verbatim as `ServiceWorkerJS` (pwa.go,
  pwa_service_worker.js, a copy of the reference's file).
- The Jbuilder views are Go functions: `MessagesByBotsIndexJSON`, `MessagesByBotsShowJSON`,
  `MessagesBoostsByBotsShowJSON` (messages_json.go), `AutocompletableUsersIndexJSON`
  (autocompletable.go), encoded as ActiveSupport::JSON.encode (railsJSON).

## Index

| Template | Go | Kind | File |
|---|---|---|---|
| accounts/_help_contact.html | `AccountsHelpContact` | func | accounts_help_contact.qtpl |
| accounts/_invite.html | `AccountsInvite` | func | accounts_invite.qtpl |
| accounts/bots/_bot.html | `AccountsBotsBot` | func | accounts_bots_bot.qtpl |
| accounts/bots/_form.html | `AccountsBotsForm` | func | accounts_bots_form.qtpl |
| accounts/bots/edit.html | `AccountsBotsEdit` | page (extends layouts/application.html) | accounts_bots_edit.qtpl |
| accounts/bots/index.html | `AccountsBotsIndex` | page (extends layouts/application.html) | accounts_bots_index.qtpl |
| accounts/bots/new.html | `AccountsBotsNew` | page (extends layouts/application.html) | accounts_bots_new.qtpl |
| accounts/custom_styles/edit.html | `AccountsCustomStylesEdit` | page (extends layouts/application.html) | accounts_custom_styles_edit.qtpl |
| accounts/edit.html | `AccountsEdit` | page (extends layouts/application.html) | accounts_edit.qtpl |
| accounts/users/_next_page_container.html | `AccountsUsersNextPageContainer` | func | accounts_users_next_page_container.qtpl |
| accounts/users/_user.html | `AccountsUsersUser` | func | accounts_users_user.qtpl |
| accounts/users/index.turbo_stream.html | `AccountsUsersIndexTurboStream` | func | accounts_users_index_turbo_stream.qtpl |
| autocompletable/users/_prompt_item.html | `AutocompletableUsersPromptItem` | func | autocompletable_users_prompt_item.qtpl |
| autocompletable/users/index.html | `AutocompletableUsersIndex` | func | autocompletable_users_index.qtpl |
| first_runs/show.html | `FirstRunsShow` | page (extends layouts/application.html) | first_runs_show.qtpl |
| messages/_actions.html | `MessagesActions` | func | messages_actions.qtpl |
| messages/_message.html | `MessagesMessage` | func | messages_message.qtpl |
| messages/_presentation.html | `MessagesPresentation` | func | messages_presentation.qtpl |
| messages/_template.html | `MessagesTemplate` | func | messages_template.qtpl |
| messages/_unrenderable.html | `MessagesUnrenderable` | func | messages_unrenderable.qtpl |
| messages/boosts/_boost.html | `MessagesBoostsBoost` | func | messages_boosts_boost.qtpl |
| messages/boosts/_boosts.html | `MessagesBoostsBoosts` | func | messages_boosts_boosts.qtpl |
| messages/boosts/index.html | `MessagesBoostsIndex` | func | messages_boosts_index.qtpl |
| messages/boosts/new.html | `MessagesBoostsNew` | func | messages_boosts_new.qtpl |
| messages/create.turbo_stream.html | `MessagesCreateTurboStream` | func | messages_create_turbo_stream.qtpl |
| messages/destroy.turbo_stream.html | `MessagesDestroyTurboStream` | func | messages_destroy_turbo_stream.qtpl |
| messages/edit.html | `MessagesEdit` | func | messages_edit.qtpl |
| messages/index.html | `MessagesIndex` | func | messages_index.qtpl |
| messages/room_not_found.html | `MessagesRoomNotFound` | func | messages_room_not_found.qtpl |
| messages/show.html | `MessagesShow` | func | messages_show.qtpl |
| pwa/_browser_settings.html | `PwaBrowserSettings` | func | pwa_browser_settings.qtpl |
| pwa/_install_instructions.html | `PwaInstallInstructions` | func | pwa_install_instructions.qtpl |
| pwa/_system_settings.html | `PwaSystemSettings` | func | pwa_system_settings.qtpl |
| pwa/manifest.json | `PwaManifestJson` | func | pwa_manifest_json.qtpl |
| rooms/closeds/_form.html | `RoomsClosedsForm` | func | rooms_closeds_form.qtpl |
| rooms/closeds/_user.html | `RoomsClosedsUser` | func | rooms_closeds_user.qtpl |
| rooms/closeds/edit.html | `RoomsClosedsEdit` | page (extends rooms/layouts/_edit.html) | rooms_closeds_edit.qtpl |
| rooms/closeds/new.html | `RoomsClosedsNew` | page (extends rooms/layouts/_new.html) | rooms_closeds_new.qtpl |
| rooms/directs/edit.html | `RoomsDirectsEdit` | page (extends layouts/application.html) | rooms_directs_edit.qtpl |
| rooms/directs/new.html | `RoomsDirectsNew` | page (extends layouts/application.html) | rooms_directs_new.qtpl |
| rooms/involvements/_bell.html | `RoomsInvolvementsBell` | func | rooms_involvements_bell.qtpl |
| rooms/involvements/show.html | `RoomsInvolvementsShow` | func | rooms_involvements_show.qtpl |
| rooms/layouts/_edit.html | `RoomsLayoutsEdit` | page (extends layouts/application.html) | rooms_layouts_edit.qtpl |
| rooms/layouts/_form.html | `RoomsLayoutsForm` | func | rooms_layouts_form.qtpl |
| rooms/layouts/_new.html | `RoomsLayoutsNew` | page (extends layouts/application.html) | rooms_layouts_new.qtpl |
| rooms/opens/_form.html | `RoomsOpensForm` | func | rooms_opens_form.qtpl |
| rooms/opens/_user.html | `RoomsOpensUser` | func | rooms_opens_user.qtpl |
| rooms/opens/edit.html | `RoomsOpensEdit` | page (extends rooms/layouts/_edit.html) | rooms_opens_edit.qtpl |
| rooms/opens/new.html | `RoomsOpensNew` | page (extends rooms/layouts/_new.html) | rooms_opens_new.qtpl |
| rooms/refreshes/show.turbo_stream.html | `RoomsRefreshesShowTurboStream` | func | rooms_refreshes_show_turbo_stream.qtpl |
| rooms/show.html | `RoomsShow` | page (extends layouts/application.html) | rooms_show.qtpl |
| rooms/show/_composer.html | `RoomsShowComposer` | func | rooms_show_composer.qtpl |
| rooms/show/_invitation.html | `RoomsShowInvitation` | func | rooms_show_invitation.qtpl |
| rooms/show/_nav.html | `RoomsShowNav` | func | rooms_show_nav.qtpl |
| searches/_recents.html | `SearchesRecents` | func | searches_recents.qtpl |
| searches/index.html | `SearchesIndex` | page (extends layouts/application.html) | searches_index.qtpl |
| sessions/incompatible_browser.html | `SessionsIncompatibleBrowser` | page (extends layouts/application.html) | sessions_incompatible_browser.qtpl |
| sessions/new.html | `SessionsNew` | page (extends layouts/application.html) | sessions_new.qtpl |
| sessions/transfers/show.html | `SessionsTransfersShow` | page (extends layouts/application.html) | sessions_transfers_show.qtpl |
| users/_ban_button.html | `UsersBanButton` | func | users_ban_button.qtpl |
| users/_mention.html | `UsersMention` | func | users_mention.qtpl |
| users/autocompletables/_template.html | `UsersAutocompletablesTemplate` | func | users_autocompletables_template.qtpl |
| users/avatars/show.svg | `UsersAvatarsShowSvg` | func | users_avatars_show_svg.qtpl |
| users/new.html | `UsersNew` | page (extends layouts/application.html) | users_new.qtpl |
| users/profiles/_membership.html | `UsersProfilesMembership` | func | users_profiles_membership.qtpl |
| users/profiles/_transfer.html | `UsersProfilesTransfer` | func | users_profiles_transfer.qtpl |
| users/profiles/show.html | `UsersProfilesShow` | page (extends layouts/application.html) | users_profiles_show.qtpl |
| users/push_subscriptions/_push_subscription.html | `UsersPushSubscriptionsPushSubscription` | func | users_push_subscriptions_push_subscription.qtpl |
| users/push_subscriptions/index.html | `UsersPushSubscriptionsIndex` | page (extends layouts/application.html) | users_push_subscriptions_index.qtpl |
| users/show.html | `UsersShow` | page (extends layouts/application.html) | users_show.qtpl |
| users/sidebars/rooms/_direct.html | `UsersSidebarsRoomsDirect` | func | users_sidebars_rooms_direct.qtpl |
| users/sidebars/rooms/_direct_placeholder.html | `UsersSidebarsRoomsDirectPlaceholder` | func | users_sidebars_rooms_direct_placeholder.qtpl |
| users/sidebars/rooms/_shared.html | `UsersSidebarsRoomsShared` | func | users_sidebars_rooms_shared.qtpl |
| users/sidebars/show.html | `UsersSidebarsShow` | page (extends layouts/application.html) | users_sidebars_show.qtpl |
| welcome/show.html | `WelcomeShow` | page (extends layouts/application.html) | welcome_show.qtpl |
| pwa/service_worker.js | `ServiceWorkerJS` (string) | embedded file, not a template | pwa_service_worker.js |
| layouts/_lightbox.html | `LayoutsLightbox` | done (layout) | layouts_lightbox.qtpl |
| layouts/application.html | `LayoutsApplication` | done (layout) | layouts_application.qtpl |
| layouts/application_wrapper.html | `LayoutsApplicationWrapper` | done (layout) | layouts_application_wrapper.qtpl |
| layouts/turbo_rails/frame.html | `LayoutsTurboRailsFrame` | done (layout) | layouts_turbo_rails_frame.qtpl |

## Templates

### accounts/_help_contact.html

- Go: `func AccountsHelpContact(ctx *ViewContext, helpContact *HelpContact)`.
- File: `internal/views/accounts_help_contact.qtpl`.
- Rust: `accounts::HelpContactPartial { ctx, help_contact: Option<HelpContact> }`.
- Types: HelpContact.
- Included by:
  - users/new.html: `{%= AccountsHelpContact(p.Ctx, p.HelpContact) %}`
  - sessions/new.html: `{%= AccountsHelpContact(p.Ctx, p.HelpContact) %}`
- `{% if let Some(owner) = help_contact %}` → `{% if helpContact != nil %}{% code owner := helpContact %}`.

### accounts/_invite.html

- Go: `func AccountsInvite(ctx *ViewContext, joinCode string)`.
- File: `internal/views/accounts_invite.qtpl`.
- Rust: `accounts::Invite { ctx, join_code }`.
- Types: —.
- Included by:
  - accounts/edit.html: `{%= AccountsInvite(p.Ctx, p.JoinCode) %}`
  - rooms/show/_invitation.html: `{%= AccountsInvite(ctx, joinCode) %}`

### accounts/bots/_bot.html

- Go: `func AccountsBotsBot(ctx *ViewContext, bot *Bot)`.
- File: `internal/views/accounts_bots_bot.qtpl`.
- Rust: `none (included only)`.
- Types: Bot, BotRoom, UserSummary (bot.User), AvatarUser.
- Included by:
  - accounts/bots/index.html: `{%= AccountsBotsBot(p.Ctx, &p.Bots[i]) %}` (for each bot)

### accounts/bots/_form.html

- Go: `func AccountsBotsForm(ctx *ViewContext, form *Form, bot *BotForm)`.
- File: `internal/views/accounts_bots_form.qtpl`.
- Rust: `none (included only)`.
- Types: Form, BotForm.
- Included by:
  - accounts/bots/new.html: `{%= AccountsBotsForm(p.Ctx, form, &p.Bot) %}` (inside the form_with(form) capture)
  - accounts/bots/edit.html: `{%= AccountsBotsForm(p.Ctx, form, &p.Bot) %}` (inside the form_with(form) capture)
- `form` is the includer's `let form = h::form_with(...)` (*Form); `bot` is the page's BotForm.

### accounts/bots/edit.html

- Go: page type `AccountsBotsEdit` (accounts.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; BotID int64; Bot BotForm.
  - Blocks (methods to fill): `(p *AccountsBotsEdit) Head()`, `(p *AccountsBotsEdit) Nav()`, `(p *AccountsBotsEdit) Content()`.
  - PageTitle: "Edit bot"; BodyClass: nil.
- File: `internal/views/accounts_bots_edit.qtpl`.
- Rust: `accounts::BotsEdit`.
- Types: BotForm, Form.
- Includes:
  - accounts/bots/_form.html: `{%= AccountsBotsForm(p.Ctx, form, &p.Bot) %}` (inside the form_with(form) capture)

### accounts/bots/index.html

- Go: page type `AccountsBotsIndex` (accounts.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; Bots []Bot.
  - Blocks (methods to fill): `(p *AccountsBotsIndex) Head()`, `(p *AccountsBotsIndex) Nav()`, `(p *AccountsBotsIndex) Content()`.
  - PageTitle: "Chat bots"; BodyClass: nil.
- File: `internal/views/accounts_bots_index.qtpl`.
- Rust: `accounts::BotsIndex`.
- Types: Bot.
- Includes:
  - accounts/bots/_bot.html: `{%= AccountsBotsBot(p.Ctx, &p.Bots[i]) %}` (for each bot)

### accounts/bots/new.html

- Go: page type `AccountsBotsNew` (accounts.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; Bot BotForm.
  - Blocks (methods to fill): `(p *AccountsBotsNew) Head()`, `(p *AccountsBotsNew) Nav()`, `(p *AccountsBotsNew) Content()`.
  - PageTitle: "New chat bot"; BodyClass: nil.
- File: `internal/views/accounts_bots_new.qtpl`.
- Rust: `accounts::BotsNew`.
- Types: BotForm, Form.
- Includes:
  - accounts/bots/_form.html: `{%= AccountsBotsForm(p.Ctx, form, &p.Bot) %}` (inside the form_with(form) capture)

### accounts/custom_styles/edit.html

- Go: page type `AccountsCustomStylesEdit` (accounts.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; CustomStyles *string.
  - Blocks (methods to fill): `(p *AccountsCustomStylesEdit) Head()`, `(p *AccountsCustomStylesEdit) Nav()`, `(p *AccountsCustomStylesEdit) Content()`.
  - PageTitle: "Custom styles"; BodyClass: nil.
- File: `internal/views/accounts_custom_styles_edit.qtpl`.
- Rust: `accounts::CustomStylesEdit`.
- Types: Form.

### accounts/edit.html

- Go: page type `AccountsEdit` (accounts.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; AccountID int64; JoinCode string; RestrictRoomCreationToAdministrators bool; Administrators []UserSummary; Members []UserSummary; NextPage *string.
  - Blocks (methods to fill): `(p *AccountsEdit) Head()`, `(p *AccountsEdit) Nav()`, `(p *AccountsEdit) Content()`, `(p *AccountsEdit) Footer()`.
  - PageTitle: "Account settings"; BodyClass: nil.
  - Method: `AccountAction() string — self.account_action()`.
- File: `internal/views/accounts_edit.qtpl`.
- Rust: `accounts::Edit`.
- Types: UserSummary, Form.
- Includes:
  - accounts/_invite.html: `{%= AccountsInvite(p.Ctx, p.JoinCode) %}`
  - accounts/users/_next_page_container.html: `{%= AccountsUsersNextPageContainer(*p.NextPage) %}` (when p.NextPage != nil)
  - accounts/users/_user.html: `{%= AccountsUsersUser(p.Ctx, &p.Administrators[i]) %}`, `{%= AccountsUsersUser(p.Ctx, &p.Members[i]) %}`
- `(!restrict_room_creation_to_administrators).to_string()` is `strconv.FormatBool(!p.RestrictRoomCreationToAdministrators)`.

### accounts/users/_next_page_container.html

- Go: `func AccountsUsersNextPageContainer(page string)`.
- File: `internal/views/accounts_users_next_page_container.qtpl`.
- Rust: `accounts::NextPageContainer { page }`.
- Types: —.
- Included by:
  - accounts/edit.html: `{%= AccountsUsersNextPageContainer(*p.NextPage) %}` (when p.NextPage != nil)
  - accounts/users/index.turbo_stream.html: `{%= AccountsUsersNextPageContainer(*nextPage) %}` (when nextPage != nil)
- `h::cgi_escape(page)` is `CGIEscape(page)`.

### accounts/users/_user.html

- Go: `func AccountsUsersUser(ctx *ViewContext, user *UserSummary)`.
- File: `internal/views/accounts_users_user.qtpl`.
- Rust: `accounts::UserPartial { ctx, user: UserSummary }`.
- Types: UserSummary, Role, AvatarUser, Form.
- Included by:
  - accounts/edit.html: `{%= AccountsUsersUser(p.Ctx, &p.Administrators[i]) %}`, `{%= AccountsUsersUser(p.Ctx, &p.Members[i]) %}`
  - accounts/users/index.turbo_stream.html: `{%= AccountsUsersUser(ctx, &users[i]) %}`
- `user.role.as_str()` is `user.Role.String()`; `h::dom_id("user", user.id, Some("role"))` is `DomID("user", user.ID, "role")`.

### accounts/users/index.turbo_stream.html

- Go: `func AccountsUsersIndexTurboStream(ctx *ViewContext, users []UserSummary, nextPage *string)`.
- File: `internal/views/accounts_users_index_turbo_stream.qtpl`.
- Rust: `accounts::UsersIndexTurboStream { ctx, users, next_page }`.
- Types: UserSummary.
- Includes:
  - accounts/users/_next_page_container.html: `{%= AccountsUsersNextPageContainer(*nextPage) %}` (when nextPage != nil)
  - accounts/users/_user.html: `{%= AccountsUsersUser(ctx, &users[i]) %}`

### autocompletable/users/_prompt_item.html

- Go: `func AutocompletableUsersPromptItem(ctx *ViewContext, user *MentionUser)`.
- File: `internal/views/autocompletable_users_prompt_item.qtpl`.
- Rust: `autocompletable::PromptItem { ctx, user: MentionUser }`.
- Types: MentionUser (embeds UserSummary), AvatarUser.
- Included by:
  - autocompletable/users/index.html: `{%= AutocompletableUsersPromptItem(ctx, &users[i]) %}`
- Includes:
  - users/_mention.html: `{%= UsersMention(ctx, user) %}`

### autocompletable/users/index.html

- Go: `func AutocompletableUsersIndex(ctx *ViewContext, users []MentionUser)`.
- File: `internal/views/autocompletable_users_index.qtpl`.
- Rust: `autocompletable::UsersIndex { ctx, users }`.
- Types: MentionUser.
- Includes:
  - autocompletable/users/_prompt_item.html: `{%= AutocompletableUsersPromptItem(ctx, &users[i]) %}`

### first_runs/show.html

- Go: page type `FirstRunsShow` (first_runs.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext.
  - Blocks (methods to fill): `(p *FirstRunsShow) Head()`, `(p *FirstRunsShow) Content()`.
  - PageTitle: "Set up Campfire"; BodyClass: "signup".
- File: `internal/views/first_runs_show.qtpl`.
- Rust: `first_runs::Show`.
- Types: Form.
- `self.page_title().unwrap_or_default()` is `*p.PageTitle()`.

### messages/_actions.html

- Go: `func MessagesActions(ctx *ViewContext, message *MessageView)`.
- File: `internal/views/messages_actions.qtpl`.
- Rust: `none (included only)`.
- Types: MessageView, AttachmentView, Reactions/Reaction.
- Included by:
  - messages/_message.html: `{%= MessagesActions(ctx, message) %}`
- `for (character, title) in crate::messages::REACTIONS` → `{% for _, reaction := range Reactions %}` (reaction.Character, reaction.Title).
- `if let Some(attachment) = message.attachment()` → `{% if attachment := message.Attachment(); attachment != nil %}`.

### messages/_message.html

- Go: `func MessagesMessage(ctx *ViewContext, message *MessageView)`.
- File: `internal/views/messages_message.qtpl`.
- Rust: `messages::MessagePartial { ctx, message }`.
- Types: MessageView, UserView.
- Included by:
  - messages/show.html: `CachedMessage(ctx, message)` (not included: rendered through the fragment cache, key `views/messages/_message:<digest>/messages/<id>-<version>/presentation-v3`; likewise CachedMessageItem from messages/index, messages/create.turbo_stream, rooms/show, rooms/refreshes/show.turbo_stream and searches/index)
- Includes:
  - messages/_actions.html: `{%= MessagesActions(ctx, message) %}`
  - messages/_presentation.html: `{%= MessagesPresentation(ctx, message) %}`
  - messages/_unrenderable.html: `{%= MessagesUnrenderable() %}`
  - messages/boosts/_boosts.html: `{%= MessagesBoostsBoosts(ctx, message) %}`
- Fragment content may not depend on the request (cached once for every host).

### messages/_presentation.html

- Go: `func MessagesPresentation(ctx *ViewContext, message *MessageView)`.
- File: `internal/views/messages_presentation.qtpl`.
- Rust: `messages::PresentationPartial { ctx, message }`.
- Types: MessageView.
- Included by:
  - messages/_message.html: `{%= MessagesPresentation(ctx, message) %}`
- `crate::messages::presentation::message_presentation(ctx, message)|safe` → `{%s= string(MessagePresentation(ctx, message)) %}`.

### messages/_template.html

- Go: `func MessagesTemplate(ctx *ViewContext, user *UserView)`.
- File: `internal/views/messages_template.qtpl`.
- Rust: `none (included only)`.
- Types: UserView.
- Included by:
  - rooms/show.html: `{%= MessagesTemplate(p.Ctx, &p.Show.User) %}` (content block; Rust `let user = &show.user`)

### messages/_unrenderable.html

- Go: `func MessagesUnrenderable()`.
- File: `internal/views/messages_unrenderable.qtpl`.
- Rust: `messages::Unrenderable`.
- Types: —.
- Included by:
  - messages/_message.html: `{%= MessagesUnrenderable() %}`

### messages/boosts/_boost.html

- Go: `func MessagesBoostsBoost(ctx *ViewContext, boost *BoostView)`.
- File: `internal/views/messages_boosts_boost.qtpl`.
- Rust: `messages::BoostPartial { ctx, boost }`.
- Types: BoostView, UserView.
- Included by:
  - messages/boosts/_boosts.html: `CachedBoost(ctx, &message.Boosts[i])` (not included: rendered through the fragment cache, key `views/messages/boosts/_boost:<digest>/boosts/<id>-<version>`)

### messages/boosts/_boosts.html

- Go: `func MessagesBoostsBoosts(ctx *ViewContext, message *MessageView)`.
- File: `internal/views/messages_boosts_boosts.qtpl`.
- Rust: `messages::BoostsPartial { ctx, message }`.
- Types: MessageView, BoostView.
- Included by:
  - messages/_message.html: `{%= MessagesBoostsBoosts(ctx, message) %}`
  - messages/boosts/index.html: `{%= MessagesBoostsBoosts(ctx, message) %}`
- Includes:
  - messages/boosts/_boost.html: `CachedBoost(ctx, &message.Boosts[i])` (not included: rendered through the fragment cache, key `views/messages/boosts/_boost:<digest>/boosts/<id>-<version>`)
- `{{ crate::messages::cached_boost(ctx, boost) }}` → `{%s= CachedBoost(ctx, &message.Boosts[i]).HTML %}` (copied as text, not recorded).

### messages/boosts/index.html

- Go: `func MessagesBoostsIndex(ctx *ViewContext, message *MessageView)`.
- File: `internal/views/messages_boosts_index.qtpl`.
- Rust: `messages::BoostsIndex { ctx, message }`.
- Types: MessageView.
- Includes:
  - messages/boosts/_boosts.html: `{%= MessagesBoostsBoosts(ctx, message) %}`

### messages/boosts/new.html

- Go: `func MessagesBoostsNew(ctx *ViewContext, message *MessageView, user *UserView)`.
- File: `internal/views/messages_boosts_new.qtpl`.
- Rust: `messages::NewBoost { ctx, message, user }`.
- Types: MessageView, UserView.

### messages/create.turbo_stream.html

- Go: `func MessagesCreateTurboStream(ctx *ViewContext, message MessageItem, roomKind RoomKind)`.
- File: `internal/views/messages_create_turbo_stream.qtpl`.
- Rust: `messages::CreateStream { ctx, message: &MessageItem, room_kind }`.
- Types: MessageItem, RoomKind.
- `crate::messages::room_dom_id(*room_kind, message.room_id(), "messages")` → `RoomDomID(roomKind, message.RoomID(), "messages")`.
- `crate::messages::cached_message_item(ctx, message)` → `{%= Fragment(CachedMessageItem(ctx, message)) %}`.

### messages/destroy.turbo_stream.html

- Go: `func MessagesDestroyTurboStream(message *MessageView)`.
- File: `internal/views/messages_destroy_turbo_stream.qtpl`.
- Rust: `messages::DestroyStream { message }`.
- Types: MessageView.

### messages/edit.html

- Go: `func MessagesEdit(ctx *ViewContext, edit *MessageEditView)`.
- File: `internal/views/messages_edit.qtpl`.
- Rust: `messages::Edit { ctx, edit: &EditView }`.
- Types: MessageEditView, MessageView, AttachmentView.
- `let message = &edit.message` → `{% code message := &edit.Message %}`.
- `attachment_presentation(ctx, attachment)|safe` → `{%s= string(AttachmentPresentation(ctx, attachment)) %}`; `crate::rooms::mention_prompt_src(*message.room_id)` → `MentionPromptSrc(message.RoomID)`.

### messages/index.html

- Go: `func MessagesIndex(ctx *ViewContext, messages []MessageItem)`.
- File: `internal/views/messages_index.qtpl`.
- Rust: `messages::Index { ctx, messages: &[MessageItem] }`.
- Types: MessageItem.
- `{{ crate::messages::cached_message_item(ctx, message) }}` → `{%= Fragment(CachedMessageItem(ctx, message)) %}` (recorded).

### messages/room_not_found.html

- Go: `func MessagesRoomNotFound()`.
- File: `internal/views/messages_room_not_found.qtpl`.
- Rust: `messages::RoomNotFound`.
- Types: —.

### messages/show.html

- Go: `func MessagesShow(ctx *ViewContext, message *MessageView)`.
- File: `internal/views/messages_show.qtpl`.
- Rust: `messages::Show { ctx, message }`.
- Types: MessageView.
- Includes:
  - messages/_message.html: `CachedMessage(ctx, message)` (not included: rendered through the fragment cache, key `views/messages/_message:<digest>/messages/<id>-<version>/presentation-v3`; likewise CachedMessageItem from messages/index, messages/create.turbo_stream, rooms/show, rooms/refreshes/show.turbo_stream and searches/index)
- Has no `extends`: the caller wraps it in the application layout (ApplicationContent).
- `{{ crate::messages::cached_message(ctx, message) }}` → `{%s= CachedMessage(ctx, message).HTML %}` (the reference copies this one as text, so it is not recorded).

### pwa/_browser_settings.html

- Go: `func PwaBrowserSettings(ctx *ViewContext)`.
- File: `internal/views/pwa_browser_settings.qtpl`.
- Rust: `pwa::BrowserSettings { ctx }`.
- Types: Platform (ctx.Platform).
- Included by:
  - rooms/involvements/_bell.html: `{%= PwaBrowserSettings(ctx) %}`

### pwa/_install_instructions.html

- Go: `func PwaInstallInstructions(ctx *ViewContext)`.
- File: `internal/views/pwa_install_instructions.qtpl`.
- Rust: `pwa::InstallInstructions { ctx }`.
- Types: Platform (ctx.Platform).
- Included by:
  - rooms/involvements/_bell.html: `{%= PwaInstallInstructions(ctx) %}`
  - users/profiles/show.html: `{%= PwaInstallInstructions(p.Ctx) %}`

### pwa/_system_settings.html

- Go: `func PwaSystemSettings(ctx *ViewContext)`.
- File: `internal/views/pwa_system_settings.qtpl`.
- Rust: `pwa::SystemSettings { ctx }`.
- Types: Platform (ctx.Platform).
- Included by:
  - rooms/involvements/_bell.html: `{%= PwaSystemSettings(ctx) %}`

### pwa/manifest.json

- Go: `func PwaManifestJson(accountName *string, logoPathSmall, logoPath, baseURL string, assetPath func(string) string)`.
- File: `internal/views/pwa_manifest_json.qtpl`.
- Rust: `pwa::Manifest { account_name, logo_path_small, logo_path, base_url, asset_path }`.
- Types: —.
- `self.json(v)` → `{%s= string(ManifestJSON(v)) %}`; `self.image_url(src)` → `ManifestImageURL(baseURL, assetPath, src)`.
- `account_name.as_deref().unwrap_or("Campfire")`: "Campfire" when accountName is nil.

### rooms/closeds/_form.html

- Go: `func RoomsClosedsForm(ctx *ViewContext, form *ClosedFormView, typeChangePath string)`.
- File: `internal/views/rooms_closeds_form.qtpl`.
- Rust: `none (included only)`.
- Types: ClosedFormView, FormRoom, UserView, RoomKind.
- Included by:
  - rooms/closeds/new.html: `{%= RoomsClosedsForm(p.Ctx, p.Form, RouteNewRoomsOpen()) %}`
  - rooms/closeds/edit.html: `{%= RoomsClosedsForm(p.Ctx, p.Form, RouteEditRoomsOpen(p.RoomID())) %}`
- Includes:
  - rooms/closeds/_user.html: `{%= RoomsClosedsUser(ctx, form, &form.SelectedUsers[i], true) %}`, `{%= RoomsClosedsUser(ctx, form, &form.UnselectedUsers[i], false) %}` (Rust `let selected = true` / `false`)
  - rooms/layouts/_form.html: `RoomForm(captureN, ctx, &form.Room, form.CanAdminister, RoomKindClosed)` (the room_form filter)
- `filter room_form(ctx, form.room, form.can_administer, RoomKind::Closed)` → capture, then `{%s= string(RoomForm(captureN, ctx, &form.Room, form.CanAdminister, RoomKindClosed)) %}` (or stream `RoomsLayoutsForm(ctx, &form.Room, form.CanAdminister, RoomKindClosed, captureN)`).
- `filter user_filter_menu_tag` → `UserFilterMenuTag(captureN)`; `h::user_filter_search_tag()` → `UserFilterSearchTag()`.

### rooms/closeds/_user.html

- Go: `func RoomsClosedsUser(ctx *ViewContext, form *ClosedFormView, user *UserView, selected bool)`.
- File: `internal/views/rooms_closeds_user.qtpl`.
- Rust: `none (included only)`.
- Types: ClosedFormView, UserView.
- Included by:
  - rooms/closeds/_form.html: `{%= RoomsClosedsUser(ctx, form, &form.SelectedUsers[i], true) %}`, `{%= RoomsClosedsUser(ctx, form, &form.UnselectedUsers[i], false) %}` (Rust `let selected = true` / `false`)
- `user.name.to_lowercase()` is `strings.ToLower(user.Name)` (Rust lowercases full Unicode; check non-ASCII edge cases such as final sigma).

### rooms/closeds/edit.html

- Go: page type `RoomsClosedsEdit` (rooms.go), extends `rooms/layouts/_edit.html`.
  - Fields: RoomsLayoutsEdit (embedded: Ctx, Room, CanAdminister, Page); Form *ClosedFormView.
  - Blocks (methods to fill): `(p *RoomsClosedsEdit) RoomForm()`.
  - PageTitle: "Edit settings for " + form.room.name (or ""); BodyClass: nil.
- File: `internal/views/rooms_closeds_edit.qtpl`.
- Rust: `rooms::ClosedsEdit`.
- Types: ClosedFormView.
- Includes:
  - rooms/closeds/_form.html: `{%= RoomsClosedsForm(p.Ctx, p.Form, RouteEditRoomsOpen(p.RoomID())) %}`
- Build with NewRoomsClosedsEdit(ctx, form), which points the layout at the page.
- `self.room_id()` is `p.RoomID()` (promoted from RoomsLayoutsEdit).

### rooms/closeds/new.html

- Go: page type `RoomsClosedsNew` (rooms.go), extends `rooms/layouts/_new.html`.
  - Fields: RoomsLayoutsNew (embedded: Ctx, Page); Form *ClosedFormView.
  - Blocks (methods to fill): `(p *RoomsClosedsNew) RoomForm()`.
  - PageTitle: "New chat room"; BodyClass: nil.
- File: `internal/views/rooms_closeds_new.qtpl`.
- Rust: `rooms::ClosedsNew`.
- Types: ClosedFormView.
- Includes:
  - rooms/closeds/_form.html: `{%= RoomsClosedsForm(p.Ctx, p.Form, RouteNewRoomsOpen()) %}`
- Build with NewRoomsClosedsNew(ctx, form).

### rooms/directs/edit.html

- Go: page type `RoomsDirectsEdit` (rooms.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; Edit *DirectEditView.
  - Blocks (methods to fill): `(p *RoomsDirectsEdit) Head()`, `(p *RoomsDirectsEdit) Nav()`, `(p *RoomsDirectsEdit) Content()`.
  - PageTitle: "Edit settings for " + edit.DisplayName; BodyClass: nil.
- File: `internal/views/rooms_directs_edit.qtpl`.
- Rust: `rooms::DirectsEdit`.
- Types: DirectEditView, UserView.

### rooms/directs/new.html

- Go: page type `RoomsDirectsNew` (rooms.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext.
  - Blocks (methods to fill): `(p *RoomsDirectsNew) Head()`, `(p *RoomsDirectsNew) Content()`.
- File: `internal/views/rooms_directs_new.qtpl`.
- Rust: `rooms::DirectsNew`.
- Types: —.
- Includes:
  - users/autocompletables/_template.html: `{%= UsersAutocompletablesTemplate(p.Ctx) %}`

### rooms/involvements/_bell.html

- Go: `func RoomsInvolvementsBell(ctx *ViewContext, room *RoomView)`.
- File: `internal/views/rooms_involvements_bell.qtpl`.
- Rust: `none (included only)`.
- Types: RoomView.
- Included by:
  - rooms/show/_nav.html: `{%= RoomsInvolvementsBell(ctx, room) %}`
- Includes:
  - pwa/_browser_settings.html: `{%= PwaBrowserSettings(ctx) %}`
  - pwa/_install_instructions.html: `{%= PwaInstallInstructions(ctx) %}`
  - pwa/_system_settings.html: `{%= PwaSystemSettings(ctx) %}`

### rooms/involvements/show.html

- Go: `func RoomsInvolvementsShow(ctx *ViewContext, involvement *InvolvementView)`.
- File: `internal/views/rooms_involvements_show.qtpl`.
- Rust: `rooms::InvolvementShow { ctx, involvement }`.
- Types: InvolvementView, RoomKind.
- `self.button()` → `{%s= string(involvement.Button(ctx)) %}`; `crate::messages::room_dom_id(*involvement.kind, *involvement.room_id, "involvement")` → `RoomDomID(involvement.Kind, involvement.RoomID, "involvement")`.

### rooms/layouts/_edit.html

- Go: page type `RoomsLayoutsEdit` (rooms.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; Room *FormRoom; CanAdminister bool; Page RoomFormPage.
  - Blocks (methods to fill): `(p *RoomsLayoutsEdit) Head()`, `(p *RoomsLayoutsEdit) Nav()`, `(p *RoomsLayoutsEdit) Content()`.
  - Method: `RoomID() int64 — self.room_id()`.
- File: `internal/views/rooms_layouts_edit.qtpl`.
- Rust: `none (askama parent template of rooms::OpensEdit/ClosedsEdit)`.
- Types: FormRoom, RoomFormPage.
- `{% block room_form %}{% endblock %}` inside content → `{%= p.Page.RoomForm() %}` (askama2qtpl emits `p.Room_form()`).
- `form.can_administer` → `p.CanAdminister`; `form.room.display_name()` → `p.Room.DisplayName()`; `crate::rooms::button_to_delete_room(ctx, self.room_id(), ...)` → `ButtonToDeleteRoom(p.Ctx, p.RoomID(), p.Room.DisplayName())`.
- Not rendered on its own: RoomsOpensEdit and RoomsClosedsEdit embed it and get Head/Nav/Content from it.

### rooms/layouts/_form.html

- Go: `func RoomsLayoutsForm(ctx *ViewContext, room *FormRoom, canAdminister bool, kind RoomKind, content HTML)`.
- File: `internal/views/rooms_layouts_form.qtpl`.
- Rust: `rooms::FormLayout { ctx, room, can_administer, kind, content: String }`.
- Types: FormRoom, RoomKind.
- Included by:
  - rooms/opens/_form.html: `RoomForm(captureN, ctx, &form.Room, form.CanAdminister, RoomKindOpen)` (the room_form filter)
  - rooms/closeds/_form.html: `RoomForm(captureN, ctx, &form.Room, form.CanAdminister, RoomKindClosed)` (the room_form filter)
- `room.action(*kind)` → `room.Action(kind)`; `room.display_name()` → `room.DisplayName()`; `content|safe` → `{%s= string(content) %}`.
- `if let Some(name) = room.name` → `{% if room.Name != nil %}`.

### rooms/layouts/_new.html

- Go: page type `RoomsLayoutsNew` (rooms.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; Page RoomFormPage.
  - Blocks (methods to fill): `(p *RoomsLayoutsNew) Head()`, `(p *RoomsLayoutsNew) Nav()`, `(p *RoomsLayoutsNew) Content()`.
- File: `internal/views/rooms_layouts_new.qtpl`.
- Rust: `none (askama parent template of rooms::OpensNew/ClosedsNew)`.
- Types: RoomFormPage.
- `{% block room_form %}{% endblock %}` → `{%= p.Page.RoomForm() %}` (askama2qtpl emits `p.Room_form()`).
- Not rendered on its own: RoomsOpensNew and RoomsClosedsNew embed it.

### rooms/opens/_form.html

- Go: `func RoomsOpensForm(ctx *ViewContext, form *OpenFormView, typeChangePath string)`.
- File: `internal/views/rooms_opens_form.qtpl`.
- Rust: `none (included only)`.
- Types: OpenFormView, FormRoom, UserView, RoomKind.
- Included by:
  - rooms/opens/new.html: `{%= RoomsOpensForm(p.Ctx, p.Form, RouteNewRoomsClosed()) %}`
  - rooms/opens/edit.html: `{%= RoomsOpensForm(p.Ctx, p.Form, RouteEditRoomsClosed(p.RoomID())) %}`
- Includes:
  - rooms/layouts/_form.html: `RoomForm(captureN, ctx, &form.Room, form.CanAdminister, RoomKindOpen)` (the room_form filter)
  - rooms/opens/_user.html: `{%= RoomsOpensUser(ctx, form, &form.Users[i]) %}`
- `filter room_form(ctx, form.room, form.can_administer, RoomKind::Open)` → `RoomForm(captureN, ctx, &form.Room, form.CanAdminister, RoomKindOpen)`; `filter user_filter_menu_tag` → `UserFilterMenuTag(captureN)`.

### rooms/opens/_user.html

- Go: `func RoomsOpensUser(ctx *ViewContext, form *OpenFormView, user *UserView)`.
- File: `internal/views/rooms_opens_user.qtpl`.
- Rust: `none (included only)`.
- Types: OpenFormView, UserView.
- Included by:
  - rooms/opens/_form.html: `{%= RoomsOpensUser(ctx, form, &form.Users[i]) %}`
- `user.name.to_lowercase()` is `strings.ToLower(user.Name)` (see rooms/closeds/_user.html).

### rooms/opens/edit.html

- Go: page type `RoomsOpensEdit` (rooms.go), extends `rooms/layouts/_edit.html`.
  - Fields: RoomsLayoutsEdit (embedded: Ctx, Room, CanAdminister, Page); Form *OpenFormView.
  - Blocks (methods to fill): `(p *RoomsOpensEdit) RoomForm()`.
  - PageTitle: "Edit settings for " + form.room.name (or ""); BodyClass: nil.
- File: `internal/views/rooms_opens_edit.qtpl`.
- Rust: `rooms::OpensEdit`.
- Types: OpenFormView.
- Includes:
  - rooms/opens/_form.html: `{%= RoomsOpensForm(p.Ctx, p.Form, RouteEditRoomsClosed(p.RoomID())) %}`
- Build with NewRoomsOpensEdit(ctx, form).
- `self.room_id()` is `p.RoomID()`.

### rooms/opens/new.html

- Go: page type `RoomsOpensNew` (rooms.go), extends `rooms/layouts/_new.html`.
  - Fields: RoomsLayoutsNew (embedded: Ctx, Page); Form *OpenFormView.
  - Blocks (methods to fill): `(p *RoomsOpensNew) RoomForm()`.
  - PageTitle: "New chat room"; BodyClass: nil.
- File: `internal/views/rooms_opens_new.qtpl`.
- Rust: `rooms::OpensNew`.
- Types: OpenFormView.
- Includes:
  - rooms/opens/_form.html: `{%= RoomsOpensForm(p.Ctx, p.Form, RouteNewRoomsClosed()) %}`
- Build with NewRoomsOpensNew(ctx, form).

### rooms/refreshes/show.turbo_stream.html

- Go: `func RoomsRefreshesShowTurboStream(ctx *ViewContext, refresh *RefreshView)`.
- File: `internal/views/rooms_refreshes_show_turbo_stream.qtpl`.
- Rust: `rooms::RefreshShow { ctx, refresh }`.
- Types: RefreshView, MessageItem, RoomKind.
- `cached_message_item` → `{%= Fragment(CachedMessageItem(ctx, message)) %}`; `room_dom_id(*refresh.room_kind, *refresh.room_id, "messages")` → `RoomDomID(refresh.RoomKind, refresh.RoomID, "messages")`.
- Byte for byte as Erubi renders it, an empty refresh is "\n" (see the template comment).

### rooms/show.html

- Go: page type `RoomsShow` (rooms.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; Show *RoomShowView.
  - Blocks (methods to fill): `(p *RoomsShow) Head()`, `(p *RoomsShow) Nav()`, `(p *RoomsShow) Sidebar()`, `(p *RoomsShow) Content()`, `(p *RoomsShow) Footer()`.
  - PageTitle: show.Room.DisplayName; BodyClass: "sidebar".
  - Method: `LoadedAt() int64 — self.loaded_at()`.
- File: `internal/views/rooms_show.qtpl`.
- Rust: `rooms::Show`.
- Types: RoomShowView, RoomView, UserView, MessageItem.
- Includes:
  - messages/_template.html: `{%= MessagesTemplate(p.Ctx, &p.Show.User) %}` (content block; Rust `let user = &show.user`)
  - rooms/show/_composer.html: `{%= RoomsShowComposer(p.Ctx, &p.Show.Room) %}` (footer block; Rust `let room = &show.room`)
  - rooms/show/_invitation.html: `{%= RoomsShowInvitation(p.Ctx, p.Show.JoinCode) %}` (content block, when p.Show.Invitation)
  - rooms/show/_nav.html: `{%= RoomsShowNav(p.Ctx, &p.Show.Room) %}` (nav block; Rust `let room = &show.room`)
- sidebar: `h::sidebar_turbo_frame_tag(Some(...), "")` → `SidebarTurboFrameTag(Ptr(RouteUserSidebar()), "")`.
- messages: `{%= Fragment(CachedMessageItem(p.Ctx, message)) %}` (recorded).

### rooms/show/_composer.html

- Go: `func RoomsShowComposer(ctx *ViewContext, room *RoomView)`.
- File: `internal/views/rooms_show_composer.qtpl`.
- Rust: `none (included only)`.
- Types: RoomView.
- Included by:
  - rooms/show.html: `{%= RoomsShowComposer(p.Ctx, &p.Show.Room) %}` (footer block; Rust `let room = &show.room`)
- `crate::rooms::mention_prompt_src(*room.id)` → `MentionPromptSrc(room.ID)`.

### rooms/show/_invitation.html

- Go: `func RoomsShowInvitation(ctx *ViewContext, joinCode string)`.
- File: `internal/views/rooms_show_invitation.qtpl`.
- Rust: `none (included only)`.
- Types: —.
- Included by:
  - rooms/show.html: `{%= RoomsShowInvitation(p.Ctx, p.Show.JoinCode) %}` (content block, when p.Show.Invitation)
- Includes:
  - accounts/_invite.html: `{%= AccountsInvite(ctx, joinCode) %}`
- `h::account_logo_tag(ctx, Some(s))` → `AccountLogoTag(ctx, s)`.

### rooms/show/_nav.html

- Go: `func RoomsShowNav(ctx *ViewContext, room *RoomView)`.
- File: `internal/views/rooms_show_nav.qtpl`.
- Rust: `none (included only)`.
- Types: RoomView.
- Included by:
  - rooms/show.html: `{%= RoomsShowNav(p.Ctx, &p.Show.Room) %}` (nav block; Rust `let room = &show.room`)
- Includes:
  - rooms/involvements/_bell.html: `{%= RoomsInvolvementsBell(ctx, room) %}`
- `h::account_logo_tag(ctx, None)` → `AccountLogoTag(ctx, "")`.

### searches/_recents.html

- Go: `func SearchesRecents(ctx *ViewContext, index *SearchIndexView)`.
- File: `internal/views/searches_recents.qtpl`.
- Rust: `none (included only)`.
- Types: SearchIndexView.
- Included by:
  - searches/index.html: `{%= SearchesRecents(p.Ctx, p.Index) %}` (nav and sidebar blocks)
- `crate::searches::search_path(search)` → `SearchPath(search)`.

### searches/index.html

- Go: page type `SearchesIndex` (searches.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; Index *SearchIndexView.
  - Blocks (methods to fill): `(p *SearchesIndex) Head()`, `(p *SearchesIndex) Nav()`, `(p *SearchesIndex) Sidebar()`, `(p *SearchesIndex) Content()`, `(p *SearchesIndex) Footer()`.
  - PageTitle: "Search"; BodyClass: "sidebar searches".
- File: `internal/views/searches_index.qtpl`.
- Rust: `searches::Index`.
- Types: SearchIndexView, MessageItem.
- Includes:
  - searches/_recents.html: `{%= SearchesRecents(p.Ctx, p.Index) %}` (nav and sidebar blocks)
- messages: `{%= Fragment(CachedMessageItem(p.Ctx, message)) %}` (recorded).

### sessions/incompatible_browser.html

- Go: page type `SessionsIncompatibleBrowser` (sessions.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext.
  - Blocks (methods to fill): `(p *SessionsIncompatibleBrowser) Head()`, `(p *SessionsIncompatibleBrowser) Content()`.
  - PageTitle: "Campfire" when ctx.Platform.AppleMessages, else "Unsupported browser"; BodyClass: nil.
- File: `internal/views/sessions_incompatible_browser.qtpl`.
- Rust: `sessions::IncompatibleBrowser`.
- Types: BrowserVersion.
- `for (browser, version) in crate::sessions::ALLOW_BROWSER_VERSIONS` → `{% for _, v := range AllowBrowserVersions %}` (v.Browser, v.Version).

### sessions/new.html

- Go: page type `SessionsNew` (sessions.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; EmailAddress *string; HelpContact *HelpContact.
  - Blocks (methods to fill): `(p *SessionsNew) Head()`, `(p *SessionsNew) Content()`.
  - PageTitle: "Sign in"; BodyClass: nil.
- File: `internal/views/sessions_new.qtpl`.
- Rust: `sessions::New`.
- Types: HelpContact, Form.
- Includes:
  - accounts/_help_contact.html: `{%= AccountsHelpContact(p.Ctx, p.HelpContact) %}`

### sessions/transfers/show.html

- Go: page type `SessionsTransfersShow` (sessions.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; Action string.
  - Blocks (methods to fill): `(p *SessionsTransfersShow) Head()`, `(p *SessionsTransfersShow) Content()`.
- File: `internal/views/sessions_transfers_show.qtpl`.
- Rust: `sessions::TransferShow`.
- Types: Form.
- `h::form_with(action).method("put").auto_submit().open()` → `FormWith(p.Action).Method("put").AutoSubmit().Open()`.

### users/_ban_button.html

- Go: `func UsersBanButton(ctx *ViewContext, user *UserSummary)`.
- File: `internal/views/users_ban_button.qtpl`.
- Rust: `users::BanButton { ctx, user: UserSummary }`.
- Types: UserSummary.
- Included by:
  - users/show.html: `{%= UsersBanButton(p.Ctx, &p.User) %}`
- `"Ban " ~ user.name` is Go string concatenation.

### users/_mention.html

- Go: `func UsersMention(ctx *ViewContext, user *MentionUser)`.
- File: `internal/views/users_mention.qtpl`.
- Rust: `users::Mention { ctx, user: MentionUser }`.
- Types: MentionUser, AvatarUser.
- Included by:
  - autocompletable/users/_prompt_item.html: `{%= UsersMention(ctx, user) %}`

### users/autocompletables/_template.html

- Go: `func UsersAutocompletablesTemplate(ctx *ViewContext)`.
- File: `internal/views/users_autocompletables_template.qtpl`.
- Rust: `users::AutocompletableTemplate { ctx }`.
- Types: —.
- Included by:
  - rooms/directs/new.html: `{%= UsersAutocompletablesTemplate(p.Ctx) %}`

### users/avatars/show.svg

- Go: `func UsersAvatarsShowSvg(userID int64, initials string)`.
- File: `internal/views/users_avatars_show_svg.qtpl`.
- Rust: `users::AvatarSvg { user_id, initials }`.
- Types: —.
- `initials.chars().count() >= 3` → `utf8.RuneCountInString(initials) >= 3`; `h::avatar_background_color(user_id)` → `AvatarBackgroundColor(userID)`.
- askama escapes .svg output like HTML (ErbEscaper).

### users/new.html

- Go: page type `UsersNew` (users.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; JoinCode string; HelpContact *HelpContact.
  - Blocks (methods to fill): `(p *UsersNew) Head()`, `(p *UsersNew) Nav()`, `(p *UsersNew) Content()`.
  - PageTitle: "Sign up"; BodyClass: "signup".
- File: `internal/views/users_new.qtpl`.
- Rust: `users::New`.
- Types: HelpContact, Form.
- Includes:
  - accounts/_help_contact.html: `{%= AccountsHelpContact(p.Ctx, p.HelpContact) %}`

### users/profiles/_membership.html

- Go: `func UsersProfilesMembership(ctx *ViewContext, membership *ProfileMembership)`.
- File: `internal/views/users_profiles_membership.qtpl`.
- Rust: `none (included only)`.
- Types: ProfileMembership, InvolvementRoom.
- Included by:
  - users/profiles/show.html: `{%= UsersProfilesMembership(p.Ctx, &p.SharedMemberships[i]) %}`, `{%= UsersProfilesMembership(p.Ctx, &p.DirectMemberships[i]) %}`
- `h::dom_id(membership.room_param_key, membership.room_id, Some("involvement"))` → `DomID(membership.RoomParamKey, membership.RoomID, "involvement")`.

### users/profiles/_transfer.html

- Go: `func UsersProfilesTransfer(ctx *ViewContext, user *UserSummary, transferID string)`.
- File: `internal/views/users_profiles_transfer.qtpl`.
- Rust: `users::Transfer { ctx, user: UserSummary, transfer_id }`.
- Types: UserSummary.
- Included by:
  - users/profiles/show.html: `{%= UsersProfilesTransfer(p.Ctx, &p.User, p.TransferID) %}`
  - users/show.html: `{%= UsersProfilesTransfer(p.Ctx, &p.User, p.TransferID) %}`

### users/profiles/show.html

- Go: page type `UsersProfilesShow` (users.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; User UserSummary; AvatarAttached bool; TransferID string; SharedMemberships []ProfileMembership; DirectMemberships []ProfileMembership.
  - Blocks (methods to fill): `(p *UsersProfilesShow) Head()`, `(p *UsersProfilesShow) Nav()`, `(p *UsersProfilesShow) Content()`.
  - PageTitle: user.Name; BodyClass: nil.
  - Method: `ProfileForm() *Form — self.profile_form()`.
- File: `internal/views/users_profiles_show.qtpl`.
- Rust: `users::ProfileShow`.
- Types: UserSummary, ProfileMembership, Form.
- Includes:
  - pwa/_install_instructions.html: `{%= PwaInstallInstructions(p.Ctx) %}`
  - users/profiles/_membership.html: `{%= UsersProfilesMembership(p.Ctx, &p.SharedMemberships[i]) %}`, `{%= UsersProfilesMembership(p.Ctx, &p.DirectMemberships[i]) %}`
  - users/profiles/_transfer.html: `{%= UsersProfilesTransfer(p.Ctx, &p.User, p.TransferID) %}`
- `h::hidden_field_tag(name, None, attrs)` → `HiddenFieldTag(name, nil, attrs)`; `attr_opt("value", user.email_address.as_deref())` → `AttrOpt("value", user.EmailAddress)`.

### users/push_subscriptions/_push_subscription.html

- Go: `func UsersPushSubscriptionsPushSubscription(ctx *ViewContext, pushSubscription *PushSubscription)`.
- File: `internal/views/users_push_subscriptions_push_subscription.qtpl`.
- Rust: `none (included only)`.
- Types: PushSubscription.
- Included by:
  - users/push_subscriptions/index.html: `{%= UsersPushSubscriptionsPushSubscription(p.Ctx, &p.PushSubscriptions[i]) %}`

### users/push_subscriptions/index.html

- Go: page type `UsersPushSubscriptionsIndex` (users.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; PushSubscriptions []PushSubscription.
  - Blocks (methods to fill): `(p *UsersPushSubscriptionsIndex) Head()`, `(p *UsersPushSubscriptionsIndex) Nav()`, `(p *UsersPushSubscriptionsIndex) Content()`.
  - PageTitle: "Push notification subscriptions"; BodyClass: nil.
- File: `internal/views/users_push_subscriptions_index.qtpl`.
- Rust: `users::PushSubscriptionsIndex`.
- Types: PushSubscription.
- Includes:
  - users/push_subscriptions/_push_subscription.html: `{%= UsersPushSubscriptionsPushSubscription(p.Ctx, &p.PushSubscriptions[i]) %}`

### users/show.html

- Go: page type `UsersShow` (users.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; User UserSummary; TransferID string.
  - Blocks (methods to fill): `(p *UsersShow) Nav()`, `(p *UsersShow) Content()`.
  - PageTitle: user.Name; BodyClass: nil.
- File: `internal/views/users_show.qtpl`.
- Rust: `users::Show`.
- Types: UserSummary.
- Includes:
  - users/_ban_button.html: `{%= UsersBanButton(p.Ctx, &p.User) %}`
  - users/profiles/_transfer.html: `{%= UsersProfilesTransfer(p.Ctx, &p.User, p.TransferID) %}`
- Defines no head block (PageBase's empty one).
- `h::rooms_directs_with_user(user.id)` → `RoomsDirectsWithUser(user.ID)`; `h::mail_to(user.email_address.as_deref().unwrap_or_default())` → `MailTo(deref or "")`.

### users/sidebars/rooms/_direct.html

- Go: `func UsersSidebarsRoomsDirect(ctx *ViewContext, membership *SidebarDirect)`.
- File: `internal/views/users_sidebars_rooms_direct.qtpl`.
- Rust: `users::SidebarDirectPartial { ctx, membership: SidebarDirect }`.
- Types: SidebarDirect, UserSummary.
- Included by:
  - users/sidebars/show.html: `CachedDirectRoomItem(p.Ctx, membership)` (not included: rendered through the fragment cache, key `views/users/sidebars/rooms/_direct:<digest>/memberships/<id>-<version>`)
- `membership.members.iter().take(4)` → first min(4, len) members; `membership.class_names()` / `member_initials()` → `ClassNames()` / `MemberInitials()`.

### users/sidebars/rooms/_direct_placeholder.html

- Go: `func UsersSidebarsRoomsDirectPlaceholder(ctx *ViewContext, user *UserSummary)`.
- File: `internal/views/users_sidebars_rooms_direct_placeholder.qtpl`.
- Rust: `none (included only)`.
- Types: UserSummary.
- Included by:
  - users/sidebars/show.html: `{%= UsersSidebarsRoomsDirectPlaceholder(p.Ctx, &p.DirectPlaceholderUsers[i]) %}`

### users/sidebars/rooms/_shared.html

- Go: `func UsersSidebarsRoomsShared(room *SidebarRoom)`.
- File: `internal/views/users_sidebars_rooms_shared.qtpl`.
- Rust: `users::SidebarSharedPartial { room: SidebarRoom }`.
- Types: SidebarRoom.
- Included by:
  - users/sidebars/show.html: `{%= UsersSidebarsRoomsShared(&p.OtherMemberships[i]) %}`
- `h::dom_id(room.param_key, room.id, Some("list"))` → `DomID(room.ParamKey, room.ID, "list")`.

### users/sidebars/show.html

- Go: page type `UsersSidebarsShow` (users.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; CurrentUser UserSummary; RoomsStream string; UserRoomsStream string; DirectMemberships []SidebarDirectItem; DirectPlaceholderUsers []UserSummary; OtherMemberships []SidebarRoom; CanCreateRooms bool.
  - Blocks (methods to fill): `(p *UsersSidebarsShow) Content()`.
- File: `internal/views/users_sidebars_show.qtpl`.
- Rust: `users::SidebarShow`.
- Types: UserSummary, SidebarDirectItem, SidebarRoom.
- Includes:
  - users/sidebars/rooms/_direct.html: `CachedDirectRoomItem(p.Ctx, membership)` (not included: rendered through the fragment cache, key `views/users/sidebars/rooms/_direct:<digest>/memberships/<id>-<version>`)
  - users/sidebars/rooms/_direct_placeholder.html: `{%= UsersSidebarsRoomsDirectPlaceholder(p.Ctx, &p.DirectPlaceholderUsers[i]) %}`
  - users/sidebars/rooms/_shared.html: `{%= UsersSidebarsRoomsShared(&p.OtherMemberships[i]) %}`
- `crate::users::cached_direct_room(ctx, membership)` → `{%s= CachedDirectRoomItem(p.Ctx, membership).HTML %}` (copied as text, not recorded, as the reference).
- `filter sidebar_turbo_frame_tag` → `SidebarTurboFrameTagBlock(captureN)`; `"view-transition-name: avatar-" ~ current_user.id` → string + strconv.FormatInt.

### welcome/show.html

- Go: page type `WelcomeShow` (welcome.go), extends `layouts/application.html`.
  - Fields: Ctx *ViewContext; CurrentUserName string.
  - Blocks (methods to fill): `(p *WelcomeShow) Head()`, `(p *WelcomeShow) Sidebar()`, `(p *WelcomeShow) Content()`.
  - PageTitle: "No rooms yet"; BodyClass: "sidebar".
- File: `internal/views/welcome_show.qtpl`.
- Rust: `welcome::Show`.
- Types: —.
- sidebar: `SidebarTurboFrameTag(Ptr(RouteUserSidebar()), "")`.
