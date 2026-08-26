package i18n

// PanelCatalog contains administration and settings-panel text.
type PanelCatalog struct {
	State            PanelStateCatalog
	Status           PanelStatusCatalog
	Verification     PanelVerificationCatalog
	RichText         PanelRichTextCatalog
	NameSpoiler      PanelNameSpoilerCatalog
	VerificationMode PanelVerificationModeCatalog
	AutoDelete       PanelAutoDeleteCatalog
	Help             PanelHelpCatalog
	Error            PanelErrorCatalog
	Menu             PanelMenuCatalog
	Settings         PanelSettingsCatalog
}

type PanelStateCatalog struct {
	Enabled  Text
	Disabled Text
}

type PanelStatusCatalog struct {
	Ping  Format
	Stats Format
}

type PanelVerificationCatalog struct {
	Started Text
	Stopped Text
}

type PanelRichTextCatalog struct {
	Enabled  Text
	Disabled Text
}

type PanelNameSpoilerCatalog struct {
	Enabled  Text
	Disabled Text
}

type PanelVerificationModeCatalog struct {
	ConfigSource  Text
	RuntimeSource Text
	Current       Format
	KernelSet     Text
	Set           Format
	AutoSet       Format
	Usage         Text
}

type PanelAutoDeleteCatalog struct {
	CurrentEnabled  Format
	CurrentDisabled Text
	Disabled        Text
	Enabled         Format
	Set             Format
	Usage           Text
}

type PanelHelpCatalog struct {
	Member            Text
	GroupState        Format
	Admin             Format
	DirectMessageNote Format
}

type PanelErrorCatalog struct {
	SaveSettings Text
	AdminOnly    Text
}

// PanelMenuCatalog contains command-menu labels owned by the panel subsystem.
type PanelMenuCatalog struct {
	Settings Text
}

// PanelSettingsCatalog contains every in-Telegram settings-panel surface.
type PanelSettingsCatalog struct {
	Launch   PanelSettingsLaunchCatalog
	Common   PanelSettingsCommonCatalog
	Source   PanelSettingsSourceCatalog
	Mode     PanelSettingsModeCatalog
	Delivery PanelSettingsDeliveryCatalog
	Screen   PanelSettingsScreenCatalog
	Field    PanelSettingsFieldCatalog
	Prompt   PanelSettingsPromptCatalog
	Error    PanelSettingsErrorCatalog
	Value    PanelSettingsValueCatalog
}

type PanelSettingsLaunchCatalog struct {
	Sent Text
	Open Text
}

type PanelSettingsCommonCatalog struct {
	Back    Text
	Close   Text
	Cancel  Text
	Save    Text
	Delete  Text
	Confirm Text
	Add     Text
	Remove  Text
	Prev    Text
	Next    Text
	Refresh Text
	Edit    Text
	Disable Text
	On      Text
	Off     Text
	None    Text
}

type PanelSettingsSourceCatalog struct {
	Runtime Text
	Config  Text
	Default Text
}

type PanelSettingsModeCatalog struct {
	Kernel Text
	Quiz   Text
	Mixed  Text
}

type PanelSettingsDeliveryCatalog struct {
	Group Text
	DM    Text
	Both  Text
}

type PanelSettingsScreenCatalog struct {
	Groups         Format
	NoGroups       Text
	GroupHome      Format
	Runtime        Format
	Lists          Format
	List           Format
	Verification   Format
	Moderation     Format
	Content        Format
	QuizBank       Format
	QuizDetail     Format
	FallbackBank   Format
	FallbackDetail Format
	Channel        Format
	Confirm        Format
	Input          Format
}

type PanelSettingsFieldCatalog struct {
	Runtime                Text
	Lists                  Text
	VerificationParameters Text
	Moderation             Text
	Content                Text
	ChangeGroup            Text
	Verification           Text
	DeliveryGroup          Text
	DeliveryDM             Text
	DeliveryBoth           Text
	ModeKernel             Text
	ModeQuiz               Text
	ModeMixed              Text
	NameSpoiler            Text
	BanDuration            Text
	LookupDelete           Text
	LookupTTL              Text
	Language               Text
	LanguageZH             Text
	LanguageZHHant         Text
	LanguageEN             Text
	ChannelWhitelist       Text
	TrustedGroups          Text
	KnownChats             Text
	ChatGroup              Text
	ChatChannel            Text
	Timeout                Text
	MaxFails               Text
	RetryCooldown          Text
	VerifyInvited          Text
	PrivateRate            Text
	QuizBank               Text
	FallbackBank           Text
	RequiredChannel        Text
	EditQuestion           Text
	AddOption              Text
	AddAnswer              Text
	CorrectOption          Text
	ResetBuiltin           Text
	SetChannel             Text
	SetInvite              Text
	ClearInvite            Text
	Antispam               Text
	MuteDuration           Text
	WarnLimit              Text
	RichText               Text
	AlertChat              Text
	ClearAlertChat         Text
}

type PanelSettingsPromptCatalog struct {
	BanDuration      Text
	LookupTTL        Text
	Timeout          Text
	MaxFails         Text
	RetryCooldown    Text
	PrivateRate      Text
	QuizQuestion     Text
	QuizOption       Text
	FallbackQuestion Text
	FallbackAnswer   Text
	InviteURL        Text
	ChannelWhitelist Text
	TrustedGroup     Text
	KnownChat        Text
	RequiredChannel  Text
	MuteDuration     Text
	WarnLimit        Text
	AlertChat        Text
}

type PanelSettingsErrorCatalog struct {
	Expired                   Text
	AuthorizationLost         Text
	AuthorizationCheckFailed  Text
	ConcurrentChange          Text
	SaveFailed                Text
	SavedRenderFailed         Text
	InvalidInput              Text
	InvalidNumber             Text
	InvalidDuration           Text
	InvalidURL                Text
	InvalidChat               Text
	StaleIndex                Text
	QuestionNeedsOptions      Text
	FallbackNeedsAnswer       Text
	InputBlockedVerification  Text
	InputCanceledVerification Text
	ReplyToPrompt             Text
	ControlGroupOnly          Text
	PanelUnavailable          Text
	WhitelistUnbanFailed      Text
}

type PanelSettingsValueCatalog struct {
	Sourced          Format
	GroupButton      Format
	IDItem           Format
	QuestionItem     Format
	OptionItem       Format
	AnswerItem       Format
	Seconds          Format
	Minutes          Format
	Permanent        Text
	Durable          Text
	RuntimeOnly      Text
	Unavailable      Text
	Builtins         Text
	Custom           Text
	RequiredDisabled Text
	InviteMissing    Text
	AlertFallback    Text
}
