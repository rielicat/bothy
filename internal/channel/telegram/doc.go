// Package telegram implements core.Channel for Telegram. Owner: M9.
//
// It is a hand-written Bot API client: getUpdates long polling (offset kept
// in meta), sendMessage/editMessageText in HTML parse mode with link previews
// disabled, inline keyboards, answerCallbackQuery, sendChatAction, getFile,
// setMyCommands and getMe, honouring 429 retry_after.
package telegram
