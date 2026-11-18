import { type ListedModelsEntry, type ListedModelsWireEntry, listedModelsPayload } from "@/lib/utils/listedModels";

// Virtual MCP assignments are staged locally in the sheet and reconciled against the key's
// persisted set on Save via attach/detach calls.

/**
 * Splits a staged Virtual MCP assignment set against the persisted one into the ids to
 * attach (staged but not original) and to detach (original but not staged). Order follows
 * the input arrays; duplicates within an input are collapsed.
 */
export function diffVmcpAssignments(original: number[], staged: number[]): { toAttach: number[]; toDetach: number[] } {
	const originalSet = new Set(original);
	const stagedSet = new Set(staged);
	const toAttach = [...new Set(staged)].filter((id) => !originalSet.has(id));
	const toDetach = [...new Set(original)].filter((id) => !stagedSet.has(id));
	return { toAttach, toDetach };
}

/** Reports whether a staged assignment set differs from the persisted one, ignoring order. */
export function vmcpAssignmentsDirty(original: number[], staged: number[]): boolean {
	const { toAttach, toDetach } = diffVmcpAssignments(original, staged);
	return toAttach.length > 0 || toDetach.length > 0;
}
// delete_after_expire is tri-state server-side: null inherits client.delete_expired_virtual_keys.
// clientDefault is undefined until the client config has loaded.

/**
 * The delete_after_expire value for a create request with an expiry; undefined omits it (inherit).
 * While the default is unknown the shown value is sent, so the key never inherits one the user did not see.
 */
export function createDeleteAfterExpire(value: boolean, clientDefault: boolean | undefined): boolean | undefined {
	return clientDefault === undefined || value !== clientDefault ? value : undefined;
}

export interface UpdateDeleteAfterExpireInput {
	switchTouched: boolean;
	expiryChanged: boolean;
	hasExpiry: boolean;
	value: boolean;
	clientDefault: boolean | undefined;
	storedOverride: boolean | null | undefined;
}

/**
 * The delete_after_expire value for an update; undefined omits it (the stored value stays), null resets
 * it to inherit. Only a touched switch changes the stored value, so editing just the expiry keeps an
 * explicit override; clearing the expiry needs nothing, since the server resets the flag then.
 */
export function updateDeleteAfterExpire({
	switchTouched,
	expiryChanged,
	hasExpiry,
	value,
	clientDefault,
	storedOverride,
}: UpdateDeleteAfterExpireInput): boolean | null | undefined {
	if (!hasExpiry) return undefined;
	if (switchTouched) return clientDefault !== undefined && value === clientDefault ? null : value;
	// A key that inherits would otherwise pick up a default the user never saw.
	if (expiryChanged && storedOverride == null && clientDefault === undefined) return value;
	return undefined;
}

export interface OwnerProfileStateInput {
	/** A team, customer or business unit is selected as the key's owner. */
	hasOwner: boolean;
	/** The caller holds View on the owner's kind; without it the profile lookup is skipped. */
	canReadOwnerProfile: boolean;
	isFetching: boolean;
	isError: boolean;
	/** HTTP status of the failed lookup, when there is one. */
	errorStatus: number | undefined;
	/** The lookup returned a profile for the selected owner. */
	hasOwnerProfile: boolean;
	/** The selected owner is the key's current owner and the server flags the key as governed by it. */
	isServerFlaggedCurrentOwner: boolean;
}

/**
 * What the sheet knows about whether the selected owner's access profile governs the key.
 *
 * Only a 404 means "no profile"; any other failure, or a lookup still in flight, leaves it unknown
 * and holds Save back. A lookup the caller is not allowed to make is different: it would fail the
 * same way every time, so it must not block saving - the server's guard enforces governance either
 * way. Without the lookup the sheet still trusts the server's `is_access_profile_managed` for the
 * key's current owner, so a governed key keeps its governance fields out of the save instead of
 * having the guard refuse it.
 */
export function resolveOwnerProfileState({
	hasOwner,
	canReadOwnerProfile,
	isFetching,
	isError,
	errorStatus,
	hasOwnerProfile,
	isServerFlaggedCurrentOwner,
}: OwnerProfileStateInput): { isGoverned: boolean; failed: boolean; unknown: boolean } {
	if (!hasOwner) return { isGoverned: false, failed: false, unknown: false };
	if (!canReadOwnerProfile) return { isGoverned: isServerFlaggedCurrentOwner, failed: false, unknown: false };
	const failed = isError && errorStatus !== 404;
	return { isGoverned: hasOwnerProfile, failed, unknown: isFetching || failed };
}

// listed_models is tri-state on the wire: omitted leaves the stored block alone, [] denies every
// model, and entries replace it. Which one to send depends on the switch: while it is untouched the
// block is the stored one and is not sent at all, and switching it off drops the field.

/**
 * The `listed_models` field for a create or update body, or nothing when the block is unchanged by
 * this save. `current` is the form's block (undefined = switch off), `stored` the one the key had.
 */
export function listedModelsUpdate({
	touched,
	current,
	stored,
}: {
	touched: boolean;
	current: ListedModelsEntry[] | undefined;
	stored: ListedModelsWireEntry[] | undefined | null;
}): { listed_models?: ListedModelsWireEntry[] | null } {
	if (!touched) return {};
	if (current === undefined) {
		// Off. A field that was never on needs no instruction; one that was on is cleared.
		return stored ? { listed_models: null } : {};
	}
	return { listed_models: listedModelsPayload(current) };
}

// Client-IP controls: allowed_ips is tri-state on the wire (omitted keeps the stored list, []/null
// clears it), and the unique-IP limit rides the rate-limit row as ip_max_limit/ip_reset_duration.
// The textarea holds the list as free text; entries split on newlines and commas.

/** The allowed-IP entries staged in the textarea: trimmed, empty segments dropped, duplicates collapsed. */
export function parseAllowedIPsInput(input: string): string[] {
	const entries = input
		.split(/[\n,]/)
		.map((entry) => entry.trim())
		.filter((entry) => entry !== "");
	return [...new Set(entries)];
}

/** The textarea text for a stored allowed list; empty when the feature is off. */
export function formatAllowedIPsInput(stored: string[] | null | undefined): string {
	return stored?.join("\n") ?? "";
}

// An entry is a bare IPv4/IPv6 address or a CIDR block of either family, matching the server's
// ValidateAllowedIPs. Ports, zones and whitespace are deliberately rejected rather than trimmed:
// normalization is an enforcement concern, not something the sheet silently applies to stored data.
const IPV4_PATTERN = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;
const IPV6_CHARS = /^[0-9a-f:.]+$/i;

function isIPv4(value: string): boolean {
	const match = IPV4_PATTERN.exec(value);
	return !!match && match.slice(1).every((octet) => Number(octet) <= 255 && (octet === "0" || !octet.startsWith("0")));
}

function isIPv6(value: string): boolean {
	if (!value.includes(":")) return false;
	// An embedded IPv4 tail is accepted only in its dotted form and only as the last segment,
	// so "::ffff:1.2.3.4" passes while a port suffix ("1.2.3.4:80") does not.
	const parts = value.split(":");
	const dotted = parts.filter((part) => part.includes("."));
	if (dotted.length > 1 || (dotted.length === 1 && (!isIPv4(dotted[0]) || parts.indexOf(dotted[0]) !== parts.length - 1))) {
		return false;
	}
	return IPV6_CHARS.test(value) && parts.filter((p) => p !== "").length > 0;
}

function isIPOrCIDR(entry: string): boolean {
	const [address, prefix, ...rest] = entry.split("/");
	if (rest.length > 0) return false;
	if (address === undefined) return false;
	if (prefix !== undefined) {
		if (prefix === "" || !/^\d{1,3}$/.test(prefix)) return false;
		if (isIPv4(address)) return Number(prefix) <= 32;
		if (isIPv6(address)) return Number(prefix) <= 128;
		return false;
	}
	return isIPv4(address) || isIPv6(address);
}

/** Entries the backend would refuse, in input order, so the sheet can reject them inline. */
export function invalidAllowedIPEntries(input: string): string[] {
	return parseAllowedIPsInput(input).filter((entry) => !isIPOrCIDR(entry));
}

/** The `allowed_ips` field for a create body; undefined when nothing is staged (feature off). */
export function allowedIPsCreate(staged: string): string[] | undefined {
	const entries = parseAllowedIPsInput(staged);
	return entries.length > 0 ? entries : undefined;
}

/**
 * The `allowed_ips` field for an update body, or nothing when this save does not change the list.
 * Absent keeps the stored value, null clears it; an empty textarea on a key that never had a list
 * is not a change.
 */
export function allowedIPsUpdate({ staged, stored }: { staged: string; stored: string[] | null | undefined }): {
	allowed_ips?: string[] | null;
} {
	const entries = parseAllowedIPsInput(staged);
	const current = stored ?? [];
	if (entries.length === current.length && entries.every((entry, index) => entry === current[index])) return {};
	return { allowed_ips: entries.length > 0 ? entries : null };
}

// Create bodies have no stored row to clear, so an unset limit omits the pair; update bodies must
// send explicit nulls to drop a limit the key used to have (a nil max clears the duration server-side).

/** The unique-IP limit fields for an update body; the pair is sent or cleared together. */
export function ipRateLimitFields(
	ipMaxLimit: number | undefined,
	ipResetDuration: string | undefined,
	mode: "update",
): { ip_max_limit?: number | null; ip_reset_duration?: string | null };
/** The unique-IP limit fields for a create body, where nothing is ever cleared. */
export function ipRateLimitFields(
	ipMaxLimit: number | undefined,
	ipResetDuration: string | undefined,
	mode: "create",
): { ip_max_limit?: number; ip_reset_duration?: string };
export function ipRateLimitFields(
	ipMaxLimit: number | undefined,
	ipResetDuration: string | undefined,
	mode: "create" | "update",
): { ip_max_limit?: number | null; ip_reset_duration?: string | null } {
	if (ipMaxLimit !== undefined) return { ip_max_limit: ipMaxLimit, ip_reset_duration: ipResetDuration || "1h" };
	return mode === "update" ? { ip_max_limit: null, ip_reset_duration: null } : {};
}