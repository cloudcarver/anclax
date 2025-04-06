/* generated using openapi-typescript-codegen -- do not edit */
/* istanbul ignore file */
/* tslint:disable */
/* eslint-disable */
import type { Credentials } from '../models/Credentials';
import type { JWKS } from '../models/JWKS';
import type { RefreshTokenRequest } from '../models/RefreshTokenRequest';
import type { SignInRequest } from '../models/SignInRequest';
import type { CancelablePromise } from '../core/CancelablePromise';
import { OpenAPI } from '../core/OpenAPI';
import { request as __request } from '../core/request';
export class DefaultService {
    /**
     * Sign in user
     * Authenticate user and return access token
     * @param requestBody
     * @returns Credentials Successfully authenticated
     * @throws ApiError
     */
    public static signIn(
        requestBody: SignInRequest,
    ): CancelablePromise<Credentials> {
        return __request(OpenAPI, {
            method: 'POST',
            url: '/auth/sign-in',
            body: requestBody,
            mediaType: 'application/json',
            errors: {
                401: `Invalid credentials`,
            },
        });
    }
    /**
     * Refresh access token
     * Get a new access token using a refresh token
     * @param requestBody
     * @returns Credentials Successfully refreshed token
     * @throws ApiError
     */
    public static refreshToken(
        requestBody: RefreshTokenRequest,
    ): CancelablePromise<Credentials> {
        return __request(OpenAPI, {
            method: 'POST',
            url: '/auth/refresh',
            body: requestBody,
            mediaType: 'application/json',
            errors: {
                401: `Invalid or expired refresh token`,
            },
        });
    }
    /**
     * Get JWKS
     * Get the JWKS for the anchor server
     * @returns JWKS JWKS
     * @throws ApiError
     */
    public static getJwks(): CancelablePromise<JWKS> {
        return __request(OpenAPI, {
            method: 'GET',
            url: '/jwks',
        });
    }
}
