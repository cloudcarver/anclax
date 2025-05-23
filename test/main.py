import pytest
import httpx

from oapi.anchor_client.api.default import sign_in
from oapi.anchor_client.client import AuthenticatedClient, Client
from oapi.anchor_client.models.sign_in_request import SignInRequest

base_url = "http://localhost:2910/api/v1"


@pytest.fixture
def auth_client():
    response = httpx.post(
        f"{base_url}/auth/sign-in", data={"name": "test", "password": "123456"}
    )
    assert response.status_code == 200
    access_token = response.json()["accessToken"]
    return AuthenticatedClient(base_url=base_url, token=access_token)


def test_auth():
    client = Client(base_url=base_url)
    response = sign_in.sync_detailed(client=client, body=SignInRequest(name="test", password="123456"))

    assert response.status_code == 200


if __name__ == "__main__":
    pytest.main([__file__, "-s"])
