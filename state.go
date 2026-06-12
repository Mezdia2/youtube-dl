package main

import (
	"sync"
	"time"
)

type Session struct {
	URL       string
	MessageID int64
	CreatedAt time.Time
}

type PendingRequest struct {
	Text      string
	ChatID    int64
	ChatType  string
	UserID    int64
	MessageID int64
	CreatedAt time.Time
}

var sessions = struct {
	sync.RWMutex
	m map[int64]*Session
}{m: make(map[int64]*Session)}

var pendingRequests = struct {
	sync.RWMutex
	m map[int64]*PendingRequest
}{m: make(map[int64]*PendingRequest)}

const sessionTimeout = 5 * time.Minute

func SetSession(chatID int64, url string, messageID int64) {
	sessions.Lock()
	sessions.m[chatID] = &Session{URL: url, MessageID: messageID, CreatedAt: time.Now()}
	sessions.Unlock()
}

func GetSession(chatID int64) *Session {
	sessions.RLock()
	s, ok := sessions.m[chatID]
	sessions.RUnlock()
	if !ok {
		return nil
	}
	if time.Since(s.CreatedAt) > sessionTimeout {
		DelSession(chatID)
		return nil
	}
	return s
}

func DelSession(chatID int64) {
	sessions.Lock()
	delete(sessions.m, chatID)
	sessions.Unlock()
}

func SetPendingRequest(userID int64, msg *Message) {
	if msg == nil {
		return
	}
	pendingRequests.Lock()
	pendingRequests.m[userID] = &PendingRequest{
		Text:      msg.Text,
		ChatID:    msg.Chat.ID,
		ChatType:  msg.Chat.Type,
		UserID:    userID,
		MessageID: msg.MessageID,
		CreatedAt: time.Now(),
	}
	pendingRequests.Unlock()
}

func GetPendingRequest(userID int64) *PendingRequest {
	pendingRequests.RLock()
	p, ok := pendingRequests.m[userID]
	pendingRequests.RUnlock()
	if !ok {
		return nil
	}
	if time.Since(p.CreatedAt) > sessionTimeout {
		DelPendingRequest(userID)
		return nil
	}
	return p
}

func DelPendingRequest(userID int64) {
	pendingRequests.Lock()
	delete(pendingRequests.m, userID)
	pendingRequests.Unlock()
}
