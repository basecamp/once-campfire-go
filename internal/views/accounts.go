package views

import "strconv"

// Views for reference/app/views/accounts (the reference's accounts.rs).

// HelpContact is User.administrator.first, shown by accounts/_help_contact.
type HelpContact struct {
	Name         string
	EmailAddress string
}

// AccountsEdit is accounts/edit.html.erb.
type AccountsEdit struct {
	PageBase
	Ctx *ViewContext
	// Current.account.id: form_with model: @account posts to /account.<id> because the account is
	// a singular resource (a quirk the reference ships with).
	AccountID                            int64
	JoinCode                             string
	RestrictRoomCreationToAdministrators bool
	Administrators                       []UserSummary
	Members                              []UserSummary
	// @page.next_param unless @page.last?.
	NextPage *string
}

func (p *AccountsEdit) AccountAction() string {
	return RouteAccount() + "." + strconv.FormatInt(p.AccountID, 10)
}

func (p *AccountsEdit) PageTitle() *string { return Ptr("Account settings") }

// Bot is a bot, as accounts/bots/_bot and _form see it.
type Bot struct {
	User UserSummary
	// User#bot_key: "id-token".
	BotKey string
	// bot.rooms.without_directs.ordered.
	Rooms []BotRoom
}

type BotRoom struct {
	ID int64
	// room_display_name(room), the room's name for shared rooms.
	Name string
}

// BotForm is the fields accounts/bots/_form fills in.
type BotForm struct {
	Name       *string
	WebhookURL *string
	// url_for(bot.avatar) when attached.
	AvatarAttachmentURL *string
}

// AccountsBotsIndex is accounts/bots/index.html.erb.
type AccountsBotsIndex struct {
	PageBase
	Ctx  *ViewContext
	Bots []Bot
}

func (p *AccountsBotsIndex) PageTitle() *string { return Ptr("Chat bots") }

// AccountsBotsNew is accounts/bots/new.html.erb.
type AccountsBotsNew struct {
	PageBase
	Ctx *ViewContext
	Bot BotForm
}

func (p *AccountsBotsNew) PageTitle() *string { return Ptr("New chat bot") }

// AccountsBotsEdit is accounts/bots/edit.html.erb.
type AccountsBotsEdit struct {
	PageBase
	Ctx   *ViewContext
	BotID int64
	Bot   BotForm
}

func (p *AccountsBotsEdit) PageTitle() *string { return Ptr("Edit bot") }

// AccountsCustomStylesEdit is accounts/custom_styles/edit.html.erb.
type AccountsCustomStylesEdit struct {
	PageBase
	Ctx          *ViewContext
	CustomStyles *string
}

func (p *AccountsCustomStylesEdit) PageTitle() *string { return Ptr("Custom styles") }
