// BLS Utility Functions for Bridge Contract
// Handles BLS public key and signature format conversions

import { bls12_381 } from "@noble/curves/bls12-381.js";

function stripHexPrefix(input) {
    return input.startsWith("0x") ? input.slice(2) : input;
}

function isHexInput(input) {
    const value = input.trim();
    const hex = stripHexPrefix(value);
    if (value.startsWith("0x")) {
        return hex.length % 2 === 0 && /^[0-9a-fA-F]*$/.test(hex);
    }

    const byteLength = hex.length / 2;
    return hex.length % 2 === 0 &&
        [48, 96, 128, 192, 256].includes(byteLength) &&
        /^[0-9a-fA-F]+$/.test(hex);
}

function decodeBytes(input, label) {
    if (typeof input !== "string" || input.trim() === "") {
        throw new Error(`${label} is empty`);
    }

    const value = input.trim();
    if (isHexInput(value)) {
        return Buffer.from(stripHexPrefix(value), "hex");
    }

    const bytes = Buffer.from(value, "base64");
    if (bytes.length === 0) {
        throw new Error(`${label} is not valid hex or base64`);
    }
    return bytes;
}

function g1ToEip2537(bytes, label) {
    if (bytes.length === 128) {
        return Buffer.from(bytes);
    }

    let raw;
    if (bytes.length === 48) {
        const point = bls12_381.G1.Point.fromBytes(bytes);
        raw = Buffer.from(point.toBytes(false));
    } else if (bytes.length === 96) {
        raw = Buffer.from(bytes);
    } else {
        throw new Error(`${label} must be 48 compressed bytes, 96 raw uncompressed bytes, or 128 EIP-2537 bytes, got ${bytes.length}`);
    }

    if (raw.length !== 96) {
        throw new Error(`unexpected uncompressed G1 length from BLS library: ${raw.length}`);
    }

    const out = Buffer.alloc(128);
    raw.copy(out, 16, 0, 48);
    raw.copy(out, 64 + 16, 48, 96);
    return out;
}

function g2ToEip2537(bytes, label) {
    if (bytes.length === 256) {
        return Buffer.from(bytes);
    }

    let raw;
    if (bytes.length === 96) {
        const point = bls12_381.G2.Point.fromBytes(bytes);
        raw = Buffer.from(point.toBytes(false));
    } else if (bytes.length === 192) {
        raw = Buffer.from(bytes);
    } else {
        throw new Error(`${label} must be 96 compressed bytes, 192 raw uncompressed bytes, or 256 EIP-2537 bytes, got ${bytes.length}`);
    }

    if (raw.length !== 192) {
        throw new Error(`unexpected uncompressed G2 length from BLS library: ${raw.length}`);
    }

    const out = Buffer.alloc(256);
    // Noble returns [X.c1, X.c0, Y.c1, Y.c0], 48 bytes each.
    // BridgeContract expects [X.c0, X.c1, Y.c0, Y.c1], each left-padded to 64 bytes.
    raw.copy(out, 0 * 64 + 16, 48, 96);
    raw.copy(out, 1 * 64 + 16, 0, 48);
    raw.copy(out, 2 * 64 + 16, 144, 192);
    raw.copy(out, 3 * 64 + 16, 96, 144);
    return out;
}

/**
 * Convert a BLS public key to EIP-2537 hex format for contract submission.
 * Accepts Gonka's 96-byte compressed G2 key or the contract's 256-byte key.
 * @param {string} publicKey - Base64 or hex-encoded BLS public key
 * @returns {string} Hex-encoded key with 0x prefix
 * @throws {Error} If key length is unsupported
 */
function publicKeyToEip2537Hex(publicKey) {
    const buffer = decodeBytes(publicKey, "BLS public key");
    return "0x" + g2ToEip2537(buffer, "BLS public key").toString("hex");
}

// Backwards-compatible name used by existing scripts.
function base64ToHex(publicKey) {
    return publicKeyToEip2537Hex(publicKey);
}

/**
 * Convert hex-encoded BLS public key back to base64 format
 * @param {string} hexKey - Hex-encoded BLS public key (0x-prefixed, 256 bytes)
 * @returns {string} Base64-encoded key
 * @throws {Error} If key format is invalid
 */
function hexToBase64(hexKey) {
    // Remove 0x prefix if present
    const cleanHex = stripHexPrefix(hexKey.trim());
    
    // Verify hex length (256 bytes = 512 hex characters)
    if (cleanHex.length !== 512) {
        throw new Error(
            `Invalid hex key length: expected 512 characters (256 bytes), got ${cleanHex.length} characters`
        );
    }
    
    // Convert hex to Buffer
    const buffer = Buffer.from(cleanHex, 'hex');
    
    // Convert to base64
    return buffer.toString('base64');
}

/**
 * Convert a BLS signature to EIP-2537 hex format.
 * Accepts Gonka's 48-byte compressed G1 signature or the contract's 128-byte signature.
 * @param {string} signature - Base64 or hex-encoded BLS signature
 * @returns {string} Hex-encoded signature with 0x prefix
 * @throws {Error} If signature length is unsupported
 */
function signatureToEip2537Hex(signature) {
    const buffer = decodeBytes(signature, "BLS signature");
    return "0x" + g1ToEip2537(buffer, "BLS signature").toString("hex");
}

// Backwards-compatible name used by existing scripts.
function base64SignatureToHex(signature) {
    return signatureToEip2537Hex(signature);
}

/**
 * Validate that a hex string is a valid BLS public key
 * @param {string} hexKey - Hex-encoded key to validate
 * @returns {boolean} True if valid
 */
function isValidBLSPublicKey(hexKey) {
    try {
        const cleanHex = stripHexPrefix(hexKey.trim());
        return cleanHex.length === 512 && /^[0-9a-fA-F]+$/.test(cleanHex);
    } catch {
        return false;
    }
}

/**
 * Validate that a hex string is a valid BLS signature
 * @param {string} hexSig - Hex-encoded signature to validate
 * @returns {boolean} True if valid
 */
function isValidBLSSignature(hexSig) {
    try {
        const cleanHex = stripHexPrefix(hexSig.trim());
        return cleanHex.length === 256 && /^[0-9a-fA-F]+$/.test(cleanHex);
    } catch {
        return false;
    }
}

/**
 * Create an empty BLS signature (for genesis epoch validation)
 * @returns {string} Empty 128-byte signature in hex format
 */
function emptySignature() {
    return '0x' + '00'.repeat(128);
}

/**
 * Create an empty BLS public key (for testing)
 * @returns {string} Empty 256-byte public key in hex format
 */
function emptyPublicKey() {
    return '0x' + '00'.repeat(256);
}

/**
 * Display BLS key information for debugging
 * @param {string} input - Base64 or hex-encoded BLS key
 * @returns {object} Object with key information
 */
function inspectBLSKey(input) {
    try {
        const format = isHexInput(input) ? "hex" : "base64";
        const buffer = decodeBytes(input, "BLS public key");
        const converted = g2ToEip2537(buffer, "BLS public key");
        return {
            format,
            length: buffer.length,
            convertedLength: converted.length,
            valid: converted.length === 256,
            hex: "0x" + converted.toString("hex"),
            base64: buffer.toString("base64")
        };
    } catch (error) {
        return {
            format: 'unknown',
            error: error.message,
            valid: false
        };
    }
}

// Export all functions
export {
    base64ToHex,
    hexToBase64,
    base64SignatureToHex,
    publicKeyToEip2537Hex,
    signatureToEip2537Hex,
    isValidBLSPublicKey,
    isValidBLSSignature,
    emptySignature,
    emptyPublicKey,
    inspectBLSKey
};

// CLI usage examples when run directly
if (import.meta.url === `file://${process.argv[1]}`) {
    console.log("BLS Utility Functions");
    console.log("=====================\n");
    
    // Example 1: Convert base64 public key to hex
    const exampleBase64Key = "uLyVx3JCSeleqDCAdj2b0+sEzNjY8u2FD02C6s3DoxULH4TT0xuHdf0Vt67drOdzBUzKR94ui9U/sO+2HuzADeUQJysmaUjYAzXPl6e4cuP+Drvu+92IL4l90/xCyqMG";
    
    console.log("Example 1: Base64 to Hex Conversion");
    console.log("-----------------------------------");
    console.log("Input (base64):", exampleBase64Key);
    
    try {
        const hexKey = base64ToHex(exampleBase64Key);
        console.log("Output (hex):", hexKey);
        console.log("Valid:", isValidBLSPublicKey(hexKey));
        console.log();
    } catch (error) {
        console.error("Error:", error.message);
    }
    
    // Example 2: Inspect a BLS key
    console.log("Example 2: Inspect BLS Key");
    console.log("-------------------------");
    const info = inspectBLSKey(exampleBase64Key);
    console.log(JSON.stringify(info, null, 2));
    console.log();
    
    // Example 3: Empty signature for genesis
    console.log("Example 3: Empty Signature (for genesis epoch)");
    console.log("-----------------------------------------------");
    console.log(emptySignature());
    console.log();
    
    console.log("\nUsage in code:");
    console.log("import { base64ToHex } from './bls.js';");
    console.log("const hexKey = base64ToHex(yourBase64Key);");
    console.log("await bridge.setGroupKey(1, hexKey);");
}
