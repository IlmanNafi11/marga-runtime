import { describe, expect, it } from "vitest";
import {
	allowedIPsCreate,
	allowedIPsUpdate,
	createDeleteAfterExpire,
	diffVmcpAssignments,
	formatAllowedIPsInput,
	invalidAllowedIPEntries,
	ipRateLimitFields,
	listedModelsUpdate,
	parseAllowedIPsInput,
	resolveOwnerProfileState,
	updateDeleteAfterExpire,
	vmcpAssignmentsDirty,
} from "./virtualKeySheet.utils";

describe("diffVmcpAssignments", () => {
	it("returns empty diffs when nothing changed (order-insensitive)", () => {
		expect(diffVmcpAssignments([1, 2, 3], [3, 2, 1])).toEqual({ toAttach: [], toDetach: [] });
	});

	it("attaches ids added and detaches ids removed", () => {
		expect(diffVmcpAssignments([1, 2], [2, 3])).toEqual({ toAttach: [3], toDetach: [1] });
	});

	it("attaches all when starting from empty", () => {
		expect(diffVmcpAssignments([], [5, 6])).toEqual({ toAttach: [5, 6], toDetach: [] });
	});

	it("detaches all when clearing", () => {
		expect(diffVmcpAssignments([5, 6], [])).toEqual({ toAttach: [], toDetach: [5, 6] });
	});

	it("collapses duplicates within an input", () => {
		expect(diffVmcpAssignments([1, 1], [1, 2, 2])).toEqual({ toAttach: [2], toDetach: [] });
	});
});

describe("vmcpAssignmentsDirty", () => {
	it("is false for equal sets regardless of order", () => {
		expect(vmcpAssignmentsDirty([1, 2, 3], [3, 1, 2])).toBe(false);
	});

	it("is true when an id is added or removed", () => {
		expect(vmcpAssignmentsDirty([1, 2], [1, 2, 3])).toBe(true);
		expect(vmcpAssignmentsDirty([1, 2], [1])).toBe(true);
	});
});
describe("createDeleteAfterExpire", () => {
	it("omits a value matching the loaded default so the key inherits", () => {
		expect(createDeleteAfterExpire(true, true)).toBeUndefined();
		expect(createDeleteAfterExpire(false, false)).toBeUndefined();
	});

	it("sends a value that differs from the default", () => {
		expect(createDeleteAfterExpire(true, false)).toBe(true);
		expect(createDeleteAfterExpire(false, true)).toBe(false);
	});

	it("sends the shown value explicitly while the default is unknown", () => {
		// Omitting it would inherit a default the user never saw, possibly true.
		expect(createDeleteAfterExpire(false, undefined)).toBe(false);
	});
});

describe("updateDeleteAfterExpire", () => {
	const base = { switchTouched: false, expiryChanged: false, hasExpiry: true, value: true, clientDefault: true, storedOverride: null };

	it("omits the flag when nothing relevant changed", () => {
		expect(updateDeleteAfterExpire(base)).toBeUndefined();
	});

	it("keeps an explicit override matching the default when only the expiry changed", () => {
		expect(updateDeleteAfterExpire({ ...base, expiryChanged: true, storedOverride: true })).toBeUndefined();
	});

	it("omits the flag when the expiry is cleared, since the server resets it", () => {
		expect(updateDeleteAfterExpire({ ...base, expiryChanged: true, hasExpiry: false, storedOverride: true })).toBeUndefined();
	});

	it("clears the override when the user sets the switch back to the default", () => {
		expect(updateDeleteAfterExpire({ ...base, switchTouched: true, storedOverride: false })).toBeNull();
	});

	it("stores an override when the user sets the switch against the default", () => {
		expect(updateDeleteAfterExpire({ ...base, switchTouched: true, value: false })).toBe(false);
	});

	it("sends the shown value explicitly when the default is unknown", () => {
		expect(updateDeleteAfterExpire({ ...base, switchTouched: true, clientDefault: undefined, value: false })).toBe(false);
		// A new expiry on a key without an override would otherwise inherit an unseen default.
		expect(updateDeleteAfterExpire({ ...base, expiryChanged: true, clientDefault: undefined, value: false })).toBe(false);
	});
});
describe("resolveOwnerProfileState", () => {
	const base = {
		hasOwner: true,
		canReadOwnerProfile: true,
		isFetching: false,
		isError: false,
		errorStatus: undefined,
		hasOwnerProfile: false,
		isServerFlaggedCurrentOwner: false,
	};

	it("is ungoverned and settled without an owner", () => {
		expect(resolveOwnerProfileState({ ...base, hasOwner: false, hasOwnerProfile: true })).toEqual({
			isGoverned: false,
			failed: false,
			unknown: false,
		});
	});

	it("is governed when the lookup returns the owner's profile", () => {
		expect(resolveOwnerProfileState({ ...base, hasOwnerProfile: true })).toEqual({ isGoverned: true, failed: false, unknown: false });
	});

	it("reads a 404 as no profile", () => {
		expect(resolveOwnerProfileState({ ...base, isError: true, errorStatus: 404 })).toEqual({
			isGoverned: false,
			failed: false,
			unknown: false,
		});
	});

	it("holds Save back while the lookup is in flight or has failed", () => {
		expect(resolveOwnerProfileState({ ...base, isFetching: true }).unknown).toBe(true);
		expect(resolveOwnerProfileState({ ...base, isError: true, errorStatus: 500 })).toEqual({
			isGoverned: false,
			failed: true,
			unknown: true,
		});
	});

	it("never blocks saving when the caller cannot read the owner's profile", () => {
		// A skipped lookup reports no error, but a stale one must not leak through either.
		expect(resolveOwnerProfileState({ ...base, canReadOwnerProfile: false, isError: true, errorStatus: 403 })).toEqual({
			isGoverned: false,
			failed: false,
			unknown: false,
		});
	});

	it("trusts the server flag for the current owner when the profile cannot be read", () => {
		expect(resolveOwnerProfileState({ ...base, canReadOwnerProfile: false, isServerFlaggedCurrentOwner: true })).toEqual({
			isGoverned: true,
			failed: false,
			unknown: false,
		});
	});
});

describe("listedModelsUpdate", () => {
	const rows = [{ provider: "openai", model: "gpt-5.4", routing_rule: "" }];

	it("sends nothing while the switch is untouched", () => {
		expect(listedModelsUpdate({ touched: false, current: rows, stored: [{ provider: "openai", model: "gpt-4o" }] })).toEqual({});
	});

	it("sends the block as the edited list once touched", () => {
		expect(listedModelsUpdate({ touched: true, current: rows, stored: undefined })).toEqual({
			listed_models: [{ provider: "openai", model: "gpt-5.4" }],
		});
	});

	it("sends an empty array for a block switched on and left empty, never null", () => {
		expect(listedModelsUpdate({ touched: true, current: [], stored: undefined })).toEqual({ listed_models: [] });
	});

	it("drops the field to switch the block back off", () => {
		expect(listedModelsUpdate({ touched: true, current: undefined, stored: rows })).toEqual({ listed_models: null });
	});

	it("sends nothing for a block switched off that was never on", () => {
		expect(listedModelsUpdate({ touched: true, current: undefined, stored: undefined })).toEqual({});
	});
});

describe("parseAllowedIPsInput", () => {
	it("splits on newlines and commas and trims entries", () => {
		expect(parseAllowedIPsInput("10.0.0.1\n10.0.0.2")).toEqual(["10.0.0.1", "10.0.0.2"]);
		expect(parseAllowedIPsInput("10.0.0.1, 10.0.0.0/24")).toEqual(["10.0.0.1", "10.0.0.0/24"]);
		expect(parseAllowedIPsInput("  10.0.0.1 \n\n")).toEqual(["10.0.0.1"]);
	});

	it("drops empty segments and collapses duplicates", () => {
		expect(parseAllowedIPsInput("10.0.0.1,,10.0.0.1")).toEqual(["10.0.0.1"]);
		expect(parseAllowedIPsInput("")).toEqual([]);
	});
});

describe("formatAllowedIPsInput", () => {
	it("joins the stored list one entry per line", () => {
		expect(formatAllowedIPsInput(["10.0.0.1", "10.0.0.0/24"])).toBe("10.0.0.1\n10.0.0.0/24");
	});

	it("is empty when the feature is off", () => {
		expect(formatAllowedIPsInput(undefined)).toBe("");
		expect(formatAllowedIPsInput(null)).toBe("");
		expect(formatAllowedIPsInput([])).toBe("");
	});
});

describe("invalidAllowedIPEntries", () => {
	it("accepts IPv4, IPv6 and CIDRs of either family", () => {
		expect(invalidAllowedIPEntries("10.0.0.1\n2001:db8::1\n10.0.0.0/24\n2001:db8::/32")).toEqual([]);
	});

	it("rejects the entries the backend would refuse", () => {
		expect(invalidAllowedIPEntries("999.1.1.1\n10.0.0.0/33\nnot an ip\n1.2.3.4:80")).toEqual([
			"999.1.1.1",
			"10.0.0.0/33",
			"not an ip",
			"1.2.3.4:80",
		]);
	});

	it("reports the offending entries in input order, trimmed entries included", () => {
		expect(invalidAllowedIPEntries("10.0.0.1\n300.1.1.1\n::ffff:1.2.3.4")).toEqual(["300.1.1.1"]);
		expect(invalidAllowedIPEntries("")).toEqual([]);
	});
});

describe("allowedIPsCreate", () => {
	it("sends the parsed list when entries are staged", () => {
		expect(allowedIPsCreate("10.0.0.1\n10.0.0.0/24")).toEqual(["10.0.0.1", "10.0.0.0/24"]);
	});

	it("omits the field when nothing is staged", () => {
		expect(allowedIPsCreate("")).toBeUndefined();
		expect(allowedIPsCreate("  \n ")).toBeUndefined();
	});
});

describe("allowedIPsUpdate", () => {
	it("sends nothing when the staged list matches the stored one", () => {
		expect(allowedIPsUpdate({ staged: "10.0.0.1\n10.0.0.0/24", stored: ["10.0.0.1", "10.0.0.0/24"] })).toEqual({});
		// Whitespace and separators in the textarea do not count as a change.
		expect(allowedIPsUpdate({ staged: " 10.0.0.1, 10.0.0.0/24 ", stored: ["10.0.0.1", "10.0.0.0/24"] })).toEqual({});
	});

	it("sends the new list when entries are added or removed", () => {
		expect(allowedIPsUpdate({ staged: "10.0.0.2", stored: ["10.0.0.1"] })).toEqual({ allowed_ips: ["10.0.0.2"] });
	});

	it("clears with null when the staged list is empty and the key had one", () => {
		expect(allowedIPsUpdate({ staged: "", stored: ["10.0.0.1"] })).toEqual({ allowed_ips: null });
	});

	it("sends nothing when the feature was off and stays off", () => {
		expect(allowedIPsUpdate({ staged: "", stored: undefined })).toEqual({});
		expect(allowedIPsUpdate({ staged: "", stored: [] })).toEqual({});
	});

	it("turns the feature on from a key that never had it", () => {
		expect(allowedIPsUpdate({ staged: "10.0.0.1", stored: undefined })).toEqual({ allowed_ips: ["10.0.0.1"] });
	});
});

describe("ipRateLimitFields", () => {
	it("maps a set limit with its duration, defaulting the duration to 1h", () => {
		expect(ipRateLimitFields(3, "1d", "create")).toEqual({ ip_max_limit: 3, ip_reset_duration: "1d" });
		expect(ipRateLimitFields(3, undefined, "update")).toEqual({ ip_max_limit: 3, ip_reset_duration: "1h" });
	});

	it("omits the pair on create when no limit is set", () => {
		expect(ipRateLimitFields(undefined, "1h", "create")).toEqual({});
	});

	it("clears the pair with nulls on update when no limit is set", () => {
		expect(ipRateLimitFields(undefined, "1h", "update")).toEqual({ ip_max_limit: null, ip_reset_duration: null });
	});
});