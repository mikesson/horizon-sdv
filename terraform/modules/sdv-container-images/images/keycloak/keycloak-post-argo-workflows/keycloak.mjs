// Copyright (c) 2024-2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import fs from 'fs/promises';
import _ from 'lodash';
import KcAdminClient from '@keycloak/keycloak-admin-client';
import retry from 'async-retry';

const clientDumpFileName = 'client-argo-workflows.json';

const config = {
  keycloak: {
    baseUrl: process.env.PLATFORM_URL + '/auth',
    username: process.env.KEYCLOAK_USERNAME,
    password: process.env.KEYCLOAK_PASSWORD,
    realm: {
      realm: 'horizon'
    },
    // Argo Server uses this client to authenticate users via Keycloak.
    client: {
      clientId: 'argo-workflows-oauth',
      adminUrl: process.env.DOMAIN + '/workflows',
      redirectUris: [
        process.env.DOMAIN + '/workflows/oauth2/callback',
        process.env.DOMAIN + '/workflows/*'
      ],
      webOrigins: [process.env.DOMAIN],
      protocol: 'openid-connect',
      publicClient: false,
      standardFlowEnabled: true,
      directAccessGrantsEnabled: false,
      serviceAccountsEnabled: false
    },
    // Client roles created inside the argo-workflows-oauth client.
    // The built-in 'roles' client scope emits these into the 'roles' token claim.
    clientRoles: [
      'administrators',
      'developers'
    ]
  }
};

// Maps each client role to one or more realm groups.
// Realm groups (administrators, viewers, developers) are created centrally
// by the base keycloak-post job
const clientRoleToGroup = {
  administrators: ['administrators'],
  developers: ['developers']
};

const keycloakAdmin = new KcAdminClient({
  baseUrl: config.keycloak.baseUrl
});

async function waitForKeycloak(baseUrl, username, password) {
  const opts = {
    retries: 100,
    minTimeout: 2000,
    factor: 1,
    onRetry: (err) => {
      console.info(`waiting for ${baseUrl}...`, err.message);
    }
  };
  await retry(
    () => login(username, password),
    opts
  );
}

async function login(username, password)  {
  try {
    await keycloakAdmin.auth({
      'username': username,
      'password': password,
      'grantType': 'password',
      'clientId': 'admin-cli'
    });
  } catch (err) {
    throw err;
  }
}

async function getRealm(realmName)  {
  try {
    let realm = await keycloakAdmin.realms.findOne({
      realm: realmName,
    });
    keycloakAdmin.setConfig({
      realmName: realm.realm,
    });
    realm.keys = await keycloakAdmin.realms.getKeys({realm: realm.realm});
    return realm;
  } catch (err) {
    throw err;
  }
}

async function createOrUpdateClient(clientConfig, realmName) {
  try {
    let clients = await keycloakAdmin.clients.find();
    let client = _.find(clients, {clientId: clientConfig.clientId});
    if (client) {
      console.info('updating %s client', clientConfig.clientId);
      const targetClient = _.merge({}, client, clientConfig);
      await keycloakAdmin.clients.update({id: client.id, realm: realmName}, targetClient);
    } else {
      console.info('creating %s client', clientConfig.clientId);
      await keycloakAdmin.clients.create(clientConfig);
    }
    clients = await keycloakAdmin.clients.find();
    return _.find(clients, {clientId: clientConfig.clientId});
  } catch (err) {
    throw err;
  }
}

// Keycloak does not return the client secret in clients.find() / ClientRepresentation
// for confidential clients after creation. Use the credentials API (same pattern as
// keycloak-post-horizon-api/keycloak.mjs).
async function generateClientSecretFile(clientId, fileName) {
  try {
    const client = (await keycloakAdmin.clients.find({ clientId }))[0];
    if (!client) {
      throw new Error(`client ${clientId} not found, cannot dump secret file`);
    }
    let cred = await keycloakAdmin.clients.getClientSecret({ id: client.id });
    if (!cred?.value) {
      cred = await keycloakAdmin.clients.generateNewClientSecret({
        id: client.id,
      });
    }
    if (!cred?.value) {
      throw new Error(
        `client ${clientId} has no client secret after get/regenerate`,
      );
    }
    const payload = { ...client, secret: cred.value };
    console.info('dumping %s client data into json file', clientId);
    await fs.writeFile(fileName, JSON.stringify(payload));
  } catch (err) {
    throw err;
  }
}

// Disable "Full Scope Allowed" so the token only contains roles explicitly
// assigned to this client, not roles from every client in the realm.
async function DisableFullScopeIfRequired() {
  const clientId = config.keycloak.client.clientId;

  try {
    const clients = await keycloakAdmin.clients.find();
    const argoClient = clients.find(client => client.clientId === clientId);

    if (!argoClient) {
      console.error(`client "${clientId}" does not exist.`);
      return;
    }

    if (argoClient.fullScopeAllowed === false) {
      console.info(`"Full scope allowed" is already disabled for client "${clientId}".`);
    } else {
      console.log(`disabling "Full scope allowed" for client "${clientId}".`);
      await keycloakAdmin.clients.update(
        { id: argoClient.id, realm: config.keycloak.realm.realm },
        { ...argoClient, fullScopeAllowed: false }
      );
    }
  } catch (err) {
    throw err;
  }
}

// Create client roles that appear in the 'roles' token claim.
async function createClientRolesIfRequired() {
  const clientId = config.keycloak.client.clientId;
  const clientRoleNames = config.keycloak.clientRoles;

  try {
    const clients = await keycloakAdmin.clients.find();
    const argoClient = clients.find(client => client.clientId === clientId);

    if (!argoClient) {
      console.error(`client "${clientId}" does not exist.`);
      return;
    }

    const existingRoles = await keycloakAdmin.clients.listRoles({ id: argoClient.id });

    for (const roleName of clientRoleNames) {
      const roleExists = existingRoles.some(role => role.name === roleName);
      if (roleExists) {
        console.info(`client role "${roleName}" already exists for "${clientId}".`);
        continue;
      }

      await keycloakAdmin.clients.createRole({id: argoClient.id, name: roleName});
      console.log(`client role "${roleName}" created for client "${clientId}".`);
    }
  } catch (err) {
    throw err;
  }
}

// Map each client role to the corresponding realm group(s) using clientRoleToGroup.
// When a user belongs to a realm group, they automatically receive the
// mapped client roles in their token for this client.
async function mapClientRolesToGroupsIfRequired() {
  const clientId = config.keycloak.client.clientId;
  const clientRoleNames = config.keycloak.clientRoles;

  try {
    const clients = await keycloakAdmin.clients.find();
    const argoClient = clients.find(client => client.clientId === clientId);

    if (!argoClient) {
      console.error(`client "${clientId}" does not exist.`);
      return;
    }

    const allGroups = await keycloakAdmin.groups.find();

    for (const clientRoleName of clientRoleNames) {
      const clientRole = await keycloakAdmin.clients.findRole({id: argoClient.id, roleName: clientRoleName});

      if (!clientRole) {
        console.warn(`client role "${clientRoleName}" does not exist in "${clientId}".`);
        continue;
      }

      const groupNames = clientRoleToGroup[clientRoleName];
      if (!groupNames) {
        console.warn(`no group mapping defined for client role "${clientRoleName}".`);
        continue;
      }

      const names = Array.isArray(groupNames) ? groupNames : [groupNames];
      for (const groupName of names) {
        const group = allGroups.find(g => g.name === groupName);

        if (!group) {
          console.warn(`group "${groupName}" does not exist.`);
          continue;
        }

        const mappedRoles = await keycloakAdmin.groups.listClientRoleMappings({id: group.id, clientUniqueId: argoClient.id});
        const alreadyMapped = mappedRoles.some(role => role.name === clientRole.name);

        if (alreadyMapped) {
          console.info(`client role "${clientRoleName}" is already mapped to group "${groupName}".`);
          continue;
        }

        await keycloakAdmin.groups.addClientRoleMappings({
          id: group.id,
          clientUniqueId: argoClient.id,
          roles: [{
            id: clientRole.id,
            name: clientRole.name
          }]
        });
        console.log(`client role "${clientRoleName}" mapped to group "${groupName}".`);
      }
    }
  } catch (err) {
    throw err;
  }
}

// Orchestrates the full Keycloak setup for Argo Workflows:
// 1. Create/update the OIDC client
// 2. Restrict token scope to this client's own roles only
// 3. Create client roles (administrators, developers)
// 4. Map client roles to centrally-managed realm groups
// 5. Dump the client secret for configure.sh to create the K8s secret
async function configureKeycloak() {
  await waitForKeycloak(
    config.keycloak.baseUrl,
    config.keycloak.username,
    config.keycloak.password
  );
  const realm = await getRealm(config.keycloak.realm.realm);
  config.keycloak.client = await createOrUpdateClient(config.keycloak.client, realm.realm);
  await DisableFullScopeIfRequired();
  await createClientRolesIfRequired();
  await mapClientRolesToGroupsIfRequired();
  await generateClientSecretFile(
    config.keycloak.client.clientId,
    clientDumpFileName
  );
}

configureKeycloak().catch((err) => {
  console.error(err.message);
  process.exit(1);
});
