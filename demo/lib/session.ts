"use client";

import type { Contact, Profile } from "./types";

const ACTIVE_KEY = "chat-demo-profile";
const ROSTER_KEY = "chat-demo-roster";
const CONTACTS_KEY = "chat-demo-contacts";

function read<T>(key: string, fallback: T): T {
  if (typeof window === "undefined") return fallback;
  const raw = window.localStorage.getItem(key);
  if (!raw) return fallback;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return fallback;
  }
}

function write(key: string, value: unknown) {
  window.localStorage.setItem(key, JSON.stringify(value));
}

export function loadProfile(): Profile | null {
  return read<Profile | null>(ACTIVE_KEY, null);
}

/** Sets the active profile and remembers it in the roster of test users
 * this browser has created, so switching back later doesn't need a new
 * user_id to be created or pasted in. */
export function saveProfile(profile: Profile) {
  write(ACTIVE_KEY, profile);
  const roster = loadRoster();
  const idx = roster.findIndex((p) => p.userId === profile.userId);
  if (idx >= 0) roster[idx] = profile;
  else roster.push(profile);
  write(ROSTER_KEY, roster);
}

/** Switches the active identity to an existing roster entry — does not
 * create a new user. */
export function setActiveProfile(profile: Profile) {
  write(ACTIVE_KEY, profile);
}

export function clearProfile() {
  window.localStorage.removeItem(ACTIVE_KEY);
}

export function loadRoster(): Profile[] {
  return read<Profile[]>(ROSTER_KEY, []);
}

/** Adds or updates a roster entry without changing the active profile —
 * for stashing a newly created test user you don't want to switch into. */
export function addToRoster(profile: Profile): Profile[] {
  const roster = loadRoster();
  const idx = roster.findIndex((p) => p.userId === profile.userId);
  if (idx >= 0) roster[idx] = profile;
  else roster.push(profile);
  write(ROSTER_KEY, roster);
  return roster;
}

export function removeFromRoster(userId: string) {
  write(
    ROSTER_KEY,
    loadRoster().filter((p) => p.userId !== userId)
  );
}

export function loadContacts(): Contact[] {
  return read<Contact[]>(CONTACTS_KEY, []);
}

/** Adds a user_id known only by pasting it from another session — no
 * token, so it can be invited to channels but never switched to. */
export function addContact(contact: Contact) {
  const contacts = loadContacts();
  if (contacts.some((c) => c.userId === contact.userId)) return;
  write(CONTACTS_KEY, [...contacts, contact]);
}

export function removeContact(userId: string) {
  write(
    CONTACTS_KEY,
    loadContacts().filter((c) => c.userId !== userId)
  );
}
