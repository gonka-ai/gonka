// Regenerates the ADR-036 cross-check fixture used by signing_cosmjs_test.go.
//
// Setup and run:
//   npm install @cosmjs/amino@0.32.4 @cosmjs/crypto@0.32.4 @cosmjs/encoding@0.32.4
//   node adr036_cosmjs_fixture.mjs
//
// Copy the printed JSON fields into the const block in signing_cosmjs_test.go.
//
// @cosmjs/amino is the amino signing path that Keplr signArbitrary also uses,
// so a signature accepted by the Go verifier here is accepted for a
// Keplr-signed envelope.
import { Secp256k1, sha256 } from "@cosmjs/crypto";
import { toBase64, fromHex, toBech32, toUtf8 } from "@cosmjs/encoding";
import { serializeSignDoc, rawSecp256k1PubkeyToRawAddress } from "@cosmjs/amino";

// Fixed private key so the fixture is reproducible.
const privkey = fromHex("11".repeat(32));
const keypair = await Secp256k1.makeKeypair(privkey);
const compressed = Secp256k1.compressPubkey(keypair.pubkey);
const signer = toBech32("gonka", rawSecp256k1PubkeyToRawAddress(compressed));

const dataBytes = toUtf8("aps-cosmjs-cross-check");
const dataB64 = toBase64(dataBytes);

// ADR-036 sign doc: empty chain_id, account_number and sequence "0", empty
// fee, no memo, a single sign/MsgSignData message.
const signDoc = {
  chain_id: "",
  account_number: "0",
  sequence: "0",
  fee: { gas: "0", amount: [] },
  msgs: [{ type: "sign/MsgSignData", value: { signer, data: dataB64 } }],
  memo: "",
};
const signBytes = serializeSignDoc(signDoc);

const sig = await Secp256k1.createSignature(sha256(signBytes), privkey);
const sig64 = new Uint8Array([...sig.r(32), ...sig.s(32)]);

console.log(JSON.stringify({
  pubkey_b64: toBase64(compressed),
  signer,
  data_b64: dataB64,
  signdoc_b64: toBase64(signBytes),
  signdoc_text: new TextDecoder().decode(signBytes),
  signature_b64: toBase64(sig64),
}, null, 2));
