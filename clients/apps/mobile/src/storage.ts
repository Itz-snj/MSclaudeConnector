import AsyncStorage from '@react-native-async-storage/async-storage';
import * as SecureStore from 'expo-secure-store';
import type { HarnessIdentity } from '@harness/protocol';

const IDENTITY_KEY = 'harness.identity';
const LAST_SEQ_KEY = 'harness.lastSeq';

// Credentials live in the Android Keystore-backed secure store; lastSeq is
// non-secret and lives in AsyncStorage.
export async function loadIdentity(): Promise<HarnessIdentity | null> {
  try {
    const raw = await SecureStore.getItemAsync(IDENTITY_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as HarnessIdentity;
    if (!parsed || typeof parsed.credential !== 'string' || typeof parsed.url !== 'string') {
      return null;
    }
    return parsed;
  } catch {
    return null;
  }
}

export async function saveIdentity(identity: HarnessIdentity): Promise<void> {
  await SecureStore.setItemAsync(IDENTITY_KEY, JSON.stringify(identity));
}

export async function clearIdentity(): Promise<void> {
  await SecureStore.deleteItemAsync(IDENTITY_KEY);
}

export async function loadLastSeq(): Promise<number> {
  try {
    const raw = await AsyncStorage.getItem(LAST_SEQ_KEY);
    const value = raw ? Number(raw) : 0;
    return Number.isFinite(value) ? value : 0;
  } catch {
    return 0;
  }
}

export async function saveLastSeq(seq: number): Promise<void> {
  await AsyncStorage.setItem(LAST_SEQ_KEY, String(seq));
}
