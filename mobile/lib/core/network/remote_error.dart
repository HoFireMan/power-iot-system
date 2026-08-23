import 'package:dio/dio.dart';

class UnauthorizedException implements Exception {
  const UnauthorizedException();
}

bool isUnauthorizedError(Object error) =>
    error is UnauthorizedException ||
    error is DioException && error.response?.statusCode == 401;
