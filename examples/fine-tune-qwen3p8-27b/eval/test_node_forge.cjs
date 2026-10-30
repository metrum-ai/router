// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
// Synthetic RSA signature regression: no keys leave this process.
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const forge = require(process.env.NODE_FORGE_PATH || 'node-forge');
const {privateKey, publicKey} = crypto.generateKeyPairSync('rsa', {modulusLength: 2048});
const data = Buffer.from('synthetic regression fixture');
const digest = crypto.createHash('sha256').update(data).digest();
const key = forge.pki.publicKeyFromPem(publicKey.export({type:'spki',format:'pem'}));
const valid = crypto.sign('sha256', data, privateKey);
assert.equal(key.verify(digest.toString('binary'), valid.toString('binary')), true);
const a = forge.asn1;
const nullNode = () => a.create(a.Class.UNIVERSAL, a.Type.NULL, false, '');
const malformed = a.create(a.Class.UNIVERSAL, a.Type.SEQUENCE, true, [
  a.create(a.Class.UNIVERSAL, a.Type.SEQUENCE, true, [
    a.create(a.Class.UNIVERSAL, a.Type.OID, false, a.oidToDer(forge.pki.oids.sha256).getBytes()),
    nullNode(), nullNode(),
  ]),
  a.create(a.Class.UNIVERSAL, a.Type.OCTETSTRING, false, digest.toString('binary')),
]);
const encoded = Buffer.from(a.toDer(malformed).getBytes(), 'binary');
const invalid = crypto.privateEncrypt({key:privateKey, padding:crypto.constants.RSA_PKCS1_PADDING}, encoded);
let accepted = false;
try { accepted = key.verify(digest.toString('binary'), invalid.toString('binary')); } catch (_) {}
assert.equal(accepted, false, 'noncanonical DigestAlgorithm was accepted');
console.log('canonical signature accepted and noncanonical DigestAlgorithm rejected');
