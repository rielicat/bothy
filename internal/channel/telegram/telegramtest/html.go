package telegramtest

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

var allowedTags = map[string]bool{
	"b": true, "strong": true, "i": true, "em": true, "u": true, "ins": true, "s": true, "strike": true, "del": true,
	"a": true, "code": true, "pre": true, "span": true, "tg-spoiler": true, "blockquote": true, "tg-emoji": true,
}

// Visible returns the text a Telegram user sees for text sent with
// parseMode, validating HTML as strictly as Telegram does: only the
// supported tags, properly nested, nothing inside code (except code in pre),
// code and pre inside nothing else, and every "<", ">" and "&" outside
// markup escaped.
func Visible(text, parseMode string) (string, error) {
	if !strings.EqualFold(parseMode, "HTML") {
		return text, nil
	}
	var b strings.Builder
	var stack []string
	for i := 0; i < len(text); {
		switch c := text[i]; c {
		case '<':
			j := strings.IndexByte(text[i:], '>')
			if j < 0 {
				return "", parseErr("unclosed start tag at byte offset " + strconv.Itoa(i))
			}
			tag := text[i+1 : i+j]
			if name, ok := strings.CutPrefix(tag, "/"); ok {
				name = strings.ToLower(strings.TrimSpace(name))
				if len(stack) == 0 || stack[len(stack)-1] != name {
					return "", parseErr("can't find end tag corresponding to start tag \"" + name + "\"")
				}
				stack = stack[:len(stack)-1]
			} else {
				name, attrs, _ := strings.Cut(tag, " ")
				name = strings.ToLower(name)
				if !allowedTags[name] {
					return "", parseErr("unsupported start tag \"" + name + "\" at byte offset " + strconv.Itoa(i))
				}
				if n := len(stack); n > 0 {
					top := stack[n-1]
					// Code and pre can't be part of other entities either,
					// except code in pre.
					if top == "code" || (top == "pre" && name != "code") || (name == "code" || name == "pre") && top != "pre" {
						return "", parseErr("entity \"" + name + "\" can't be nested in \"" + top + "\"")
					}
				}
				if name == "a" && !strings.HasPrefix(strings.TrimSpace(attrs), `href="`) {
					return "", parseErr("a tag without href")
				}
				stack = append(stack, name)
			}
			i += j + 1
		case '&':
			j := strings.IndexByte(text[i:], ';')
			if j < 0 || j > 12 {
				return "", parseErr("unsupported entity at byte offset " + strconv.Itoa(i))
			}
			r, ok := entity(text[i+1 : i+j])
			if !ok {
				return "", parseErr("unsupported HTML entity \"" + text[i:i+j+1] + "\"")
			}
			b.WriteRune(r)
			i += j + 1
		case '>':
			return "", parseErr("unexpected end tag character at byte offset " + strconv.Itoa(i))
		default:
			_, n := utf8.DecodeRuneInString(text[i:])
			b.WriteString(text[i : i+n])
			i += n
		}
	}
	if len(stack) > 0 {
		return "", parseErr("unclosed tag \"" + stack[len(stack)-1] + "\"")
	}
	return b.String(), nil
}

func entity(name string) (rune, bool) {
	switch name {
	case "lt":
		return '<', true
	case "gt":
		return '>', true
	case "amp":
		return '&', true
	case "quot":
		return '"', true
	}
	if num, ok := strings.CutPrefix(name, "#"); ok {
		base := 10
		if hex, ok := strings.CutPrefix(num, "x"); ok {
			num, base = hex, 16
		}
		v, err := strconv.ParseInt(num, base, 32)
		if err == nil && v > 0 && utf8.ValidRune(rune(v)) {
			return rune(v), true
		}
	}
	return 0, false
}

// badRequest is a 400 answer; its text is Telegram's description.
type badRequest string

func (b badRequest) Error() string { return "Bad Request: " + string(b) }

func parseErr(detail string) error { return badRequest("can't parse entities: " + detail) }
