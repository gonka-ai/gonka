// BLS Utility Functions for Bridge Contract
// Handles BLS public key and signature format conversions

/**
 * Convert base64-encoded BLS public key to hex format for contract submission
 * @param {string} base64Key - Base64-encoded BLS public key (256 bytes, padded)
 * @returns {string} Hex-encoded key with 0x prefix
 * @throws {Error} If key length is not 256 bytes
 */
function base64ToHex(base64Key) {
    // Remove any whitespace
    const cleanKey = base64Key.trim();
    
    // Decode base64 to Buffer
    const buffer = Buffer.from(cleanKey, 'base64');
    
    // Verify it's exactly 256 bytes (Padded G2 public key for EIP-2537)
    if (buffer.length !== 256) {
        throw new Error(
            `Invalid BLS public key length: expected 256 bytes, got ${buffer.length} bytes. ` +
            `Base64 input: "${cleanKey}"`
        );
    }
    
    // Convert to hex with 0x prefix
    return '0x' + buffer.toString('hex');
}

/**
 * Convert hex-encoded BLS public key back to base64 format
 * @param {string} hexKey - Hex-encoded BLS public key (0x-prefixed, 256 bytes)
 * @returns {string} Base64-encoded key
 * @throws {Error} If key format is invalid
 */
function hexToBase64(hexKey) {
    // Remove 0x prefix if present
    const cleanHex = hexKey.startsWith('0x') ? hexKey.slice(2) : hexKey;
    
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
 * Convert base64-encoded BLS signature to hex format
 * @param {string} base64Sig - Base64-encoded BLS signature (128 bytes, padded G1 point)
 * @returns {string} Hex-encoded signature with 0x prefix
 * @throws {Error} If signature length is not 128 bytes
 */
function base64SignatureToHex(base64Sig) {
    // Remove any whitespace
    const cleanSig = base64Sig.trim();
    
    // Decode base64 to Buffer
    const buffer = Buffer.from(cleanSig, 'base64');
    
    // Verify it's exactly 128 bytes (Padded G1 signature for EIP-2537)
    if (buffer.length !== 128) {
        throw new Error(
            `Invalid BLS signature length: expected 128 bytes, got ${buffer.length} bytes. ` +
            `Base64 input: "${cleanSig}"`
        );
    }
    
    // Convert to hex with 0x prefix
    return '0x' + buffer.toString('hex');
}

/**
 * Validate that a hex string is a valid BLS public key
 * @param {string} hexKey - Hex-encoded key to validate
 * @returns {boolean} True if valid
 */
function isValidBLSPublicKey(hexKey) {
    try {
        const cleanHex = hexKey.startsWith('0x') ? hexKey.slice(2) : hexKey;
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
        const cleanHex = hexKey.startsWith('0x') ? hexKey.slice(2) : hexKey;
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
    const isHex = input.startsWith('0x') || /^[0-9a-fA-F]+$/.test(input);
    
    try {
        if (isHex) {
            const cleanHex = input.startsWith('0x') ? input.slice(2) : input;
            const buffer = Buffer.from(cleanHex, 'hex');
            return {
                format: 'hex',
                length: buffer.length,
                valid: buffer.length === 256,
                hex: '0x' + cleanHex,
                base64: buffer.toString('base64')
            };
        } else {
            const buffer = Buffer.from(input, 'base64');
            return {
                format: 'base64',
                length: buffer.length,
                valid: buffer.length === 256,
                hex: '0x' + buffer.toString('hex'),
                base64: input
            };
        }
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
    console.log("await bridge.submitGroupKey(1, hexKey, '0x');");
}


